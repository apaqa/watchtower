// Package wql - 语法分析器：将 Token 序列解析为抽象语法树（AST）
package wql

import (
	"fmt"
	"strconv"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// AST 节点定义
// ─────────────────────────────────────────────────────────────────────────────

// Node 是所有 AST 节点的接口
type Node interface {
	nodeMarker()
}

// MetricSelector 表示一个指标选择器：name{label="val",...}
type MetricSelector struct {
	Name   string
	Labels map[string]string // 标签过滤器（精确匹配）
}

func (*MetricSelector) nodeMarker() {}

// FunctionCall 表示聚合函数调用：avg(metric{labels}[5m])
// 支持的函数：avg, max, min, sum, count, rate, last
type FunctionCall struct {
	Func     string           // 函数名
	Selector *MetricSelector  // 指标选择器
	Range    time.Duration    // 时间窗口；0 表示使用默认窗口（5m）
}

func (*FunctionCall) nodeMarker() {}

// BinaryExpr 表示二元算术运算：left op right
type BinaryExpr struct {
	Left  Node
	Op    string // "+", "-", "*", "/"
	Right Node
}

func (*BinaryExpr) nodeMarker() {}

// NumberLiteral 表示数值常量
type NumberLiteral struct {
	Value float64
}

func (*NumberLiteral) nodeMarker() {}

// ComparisonExpr 表示比较表达式：expr > 80
type ComparisonExpr struct {
	Left  Node
	Op    string  // ">", ">=", "<", "<=", "==", "!="
	Right float64 // 右侧只支持数值常量
}

func (*ComparisonExpr) nodeMarker() {}

// GroupByExpr 表示分组聚合：expr by (label1, label2)
type GroupByExpr struct {
	Expr   Node
	Labels []string // 按哪些标签分组
}

func (*GroupByExpr) nodeMarker() {}

// ─────────────────────────────────────────────────────────────────────────────
// 解析器
// ─────────────────────────────────────────────────────────────────────────────

// Parser 将 Token 序列解析为 AST
type Parser struct {
	tokens []Token
	pos    int
}

// NewParser 根据已分词的 Token 切片创建解析器
func NewParser(tokens []Token) *Parser {
	return &Parser{tokens: tokens}
}

// Parse 解析整个查询，返回根节点
func (p *Parser) Parse() (Node, error) {
	node, err := p.parseArith()
	if err != nil {
		return nil, err
	}

	// 检查是否有比较运算符
	if tok := p.peek(); isCompOp(tok.Type) {
		p.consume()
		right, err := p.parseNumber()
		if err != nil {
			return nil, err
		}
		node = &ComparisonExpr{Left: node, Op: tok.Literal, Right: right}
	}

	// 检查是否有 "by" 关键字
	if p.peek().Type == TOK_IDENT && p.peek().Literal == "by" {
		p.consume() // 消耗 "by"
		labels, err := p.parseIdentList()
		if err != nil {
			return nil, err
		}
		node = &GroupByExpr{Expr: node, Labels: labels}
	}

	return node, nil
}

// parseArith 解析加减法表达式
func (p *Parser) parseArith() (Node, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.Type != TOK_PLUS && tok.Type != TOK_MINUS {
			break
		}
		p.consume()
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: tok.Literal, Right: right}
	}
	return left, nil
}

// parseTerm 解析乘除法表达式
func (p *Parser) parseTerm() (Node, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.Type != TOK_STAR && tok.Type != TOK_SLASH {
			break
		}
		p.consume()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: tok.Literal, Right: right}
	}
	return left, nil
}

// parsePrimary 解析基本单元：函数调用、指标选择器、数值、括号表达式
func (p *Parser) parsePrimary() (Node, error) {
	tok := p.peek()

	// 括号表达式
	if tok.Type == TOK_LPAREN {
		p.consume()
		inner, err := p.parseArith()
		if err != nil {
			return nil, err
		}
		if p.peek().Type != TOK_RPAREN {
			return nil, fmt.Errorf("期望 ')'，得到 %q", p.peek().Literal)
		}
		p.consume()
		return inner, nil
	}

	// 数值字面量
	if tok.Type == TOK_NUMBER {
		p.consume()
		v, err := strconv.ParseFloat(tok.Literal, 64)
		if err != nil {
			return nil, fmt.Errorf("无效数值 %q", tok.Literal)
		}
		return &NumberLiteral{Value: v}, nil
	}

	// 标识符：函数调用或指标选择器
	if tok.Type == TOK_IDENT {
		// 向前看一格判断是否是函数调用（IDENT 后面跟 LPAREN）
		if p.peekAt(1).Type == TOK_LPAREN {
			return p.parseFunctionCall()
		}
		return p.parseSelector()
	}

	return nil, fmt.Errorf("语法错误：意外的词法单元 %q", tok.Literal)
}

// parseFunctionCall 解析 fn(selector[range]) 形式
func (p *Parser) parseFunctionCall() (Node, error) {
	fnTok := p.consume() // 函数名
	p.consume()          // "("

	selector, err := p.parseSelector()
	if err != nil {
		return nil, err
	}
	sel, ok := selector.(*MetricSelector)
	if !ok {
		return nil, fmt.Errorf("函数参数必须是指标选择器")
	}

	// 可选时间范围 [5m]
	var rang time.Duration
	if p.peek().Type == TOK_LBRACKET {
		p.consume() // "["
		dur, err := p.parseDuration()
		if err != nil {
			return nil, err
		}
		rang = dur
		if p.peek().Type != TOK_RBRACKET {
			return nil, fmt.Errorf("期望 ']'，得到 %q", p.peek().Literal)
		}
		p.consume() // "]"
	}

	if p.peek().Type != TOK_RPAREN {
		return nil, fmt.Errorf("期望 ')'，得到 %q", p.peek().Literal)
	}
	p.consume() // ")"

	return &FunctionCall{Func: fnTok.Literal, Selector: sel, Range: rang}, nil
}

// parseSelector 解析 name{label="val",...} 形式
func (p *Parser) parseSelector() (Node, error) {
	nameTok := p.peek()
	if nameTok.Type != TOK_IDENT {
		return nil, fmt.Errorf("期望指标名称，得到 %q", nameTok.Literal)
	}
	p.consume()

	labels := make(map[string]string)
	if p.peek().Type == TOK_LBRACE {
		p.consume() // "{"
		// 解析标签过滤器列表
		for p.peek().Type != TOK_RBRACE && p.peek().Type != TOK_EOF {
			keyTok := p.peek()
			if keyTok.Type != TOK_IDENT {
				return nil, fmt.Errorf("期望标签键，得到 %q", keyTok.Literal)
			}
			p.consume()

			if p.peek().Type != TOK_EQ {
				return nil, fmt.Errorf("期望 '='，得到 %q", p.peek().Literal)
			}
			p.consume()

			valTok := p.peek()
			if valTok.Type != TOK_STRING {
				return nil, fmt.Errorf("期望字符串值，得到 %q", valTok.Literal)
			}
			p.consume()
			labels[keyTok.Literal] = valTok.Literal

			if p.peek().Type == TOK_COMMA {
				p.consume()
			}
		}
		if p.peek().Type != TOK_RBRACE {
			return nil, fmt.Errorf("期望 '}'，得到 %q", p.peek().Literal)
		}
		p.consume() // "}"
	}

	return &MetricSelector{Name: nameTok.Literal, Labels: labels}, nil
}

// parseDuration 解析时长 Token（如 5m、1h、15m、1d）
func (p *Parser) parseDuration() (time.Duration, error) {
	tok := p.peek()
	if tok.Type != TOK_DURATION {
		return 0, fmt.Errorf("期望时长，得到 %q", tok.Literal)
	}
	p.consume()

	// 最后一个字符是单位
	s := tok.Literal
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("无效时长 %q", s)
	}

	switch unit {
	case 's':
		return time.Duration(n) * time.Second, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("未知时间单位 %q", unit)
	}
}

// parseNumber 解析数值字面量
func (p *Parser) parseNumber() (float64, error) {
	tok := p.peek()
	if tok.Type != TOK_NUMBER {
		return 0, fmt.Errorf("期望数值，得到 %q", tok.Literal)
	}
	p.consume()
	v, err := strconv.ParseFloat(tok.Literal, 64)
	if err != nil {
		return 0, fmt.Errorf("无效数值 %q", tok.Literal)
	}
	return v, nil
}

// parseIdentList 解析括号内的标识符列表：(label1, label2, ...)
func (p *Parser) parseIdentList() ([]string, error) {
	if p.peek().Type != TOK_LPAREN {
		return nil, fmt.Errorf("期望 '('，得到 %q", p.peek().Literal)
	}
	p.consume()

	var labels []string
	for p.peek().Type != TOK_RPAREN && p.peek().Type != TOK_EOF {
		tok := p.peek()
		if tok.Type != TOK_IDENT {
			return nil, fmt.Errorf("期望标签名，得到 %q", tok.Literal)
		}
		labels = append(labels, tok.Literal)
		p.consume()
		if p.peek().Type == TOK_COMMA {
			p.consume()
		}
	}
	if p.peek().Type != TOK_RPAREN {
		return nil, fmt.Errorf("期望 ')'，得到 %q", p.peek().Literal)
	}
	p.consume()
	return labels, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助方法
// ─────────────────────────────────────────────────────────────────────────────

func (p *Parser) peek() Token {
	return p.peekAt(0)
}

func (p *Parser) peekAt(offset int) Token {
	idx := p.pos + offset
	if idx >= len(p.tokens) {
		return Token{Type: TOK_EOF}
	}
	return p.tokens[idx]
}

func (p *Parser) consume() Token {
	tok := p.tokens[p.pos]
	p.pos++
	return tok
}

func isCompOp(t TokenType) bool {
	return t == TOK_GT || t == TOK_GTE || t == TOK_LT ||
		t == TOK_LTE || t == TOK_EQEQ || t == TOK_NEQ
}

// Parse 是便捷函数：一步完成词法分析和语法分析
func Parse(query string) (Node, error) {
	l := NewLexer(query)
	tokens, err := l.Tokenize()
	if err != nil {
		return nil, fmt.Errorf("词法错误: %w", err)
	}
	p := NewParser(tokens)
	return p.Parse()
}
