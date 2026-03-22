// Package wql 实现 WatchTower 查询语言（WQL）的词法分析、语法分析和求值
// 查询语法示例：avg(cpu_usage_percent{host="DESKTOP"}[5m]) > 80
package wql

import (
	"fmt"
	"unicode"
)

// TokenType 表示词法单元的类型
type TokenType int

const (
	TOK_EOF      TokenType = iota // 输入结束
	TOK_IDENT                     // 标识符：函数名、指标名、标签键、关键字 "by"
	TOK_NUMBER                    // 数值字面量，如 80、3.14
	TOK_STRING                    // 双引号字符串（标签值），如 "DESKTOP"
	TOK_DURATION                  // 时间范围，如 5m、1h、15m、1d
	TOK_LPAREN                    // (
	TOK_RPAREN                    // )
	TOK_LBRACE                    // {
	TOK_RBRACE                    // }
	TOK_LBRACKET                  // [
	TOK_RBRACKET                  // ]
	TOK_EQ                        // =（标签过滤器中的等号）
	TOK_NEQ                       // !=
	TOK_EQEQ                      // ==（比较运算符）
	TOK_GT                        // >
	TOK_GTE                       // >=
	TOK_LT                        // <
	TOK_LTE                       // <=
	TOK_PLUS                      // +
	TOK_MINUS                     // -
	TOK_STAR                      // *
	TOK_SLASH                     // /
	TOK_COMMA                     // ,
)

// Token 是词法分析产生的一个词法单元
type Token struct {
	Type    TokenType
	Literal string // 原始文本
}

// Lexer 将 WQL 查询字符串转化为 Token 序列
type Lexer struct {
	input []rune
	pos   int
}

// NewLexer 创建一个新的词法分析器
func NewLexer(input string) *Lexer {
	return &Lexer{input: []rune(input)}
}

// Tokenize 返回所有 Token 直到 EOF
func (l *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
		if tok.Type == TOK_EOF {
			break
		}
	}
	return tokens, nil
}

// next 读取并返回下一个 Token
func (l *Lexer) next() (Token, error) {
	// 跳过空白字符
	for l.pos < len(l.input) && unicode.IsSpace(l.input[l.pos]) {
		l.pos++
	}
	if l.pos >= len(l.input) {
		return Token{Type: TOK_EOF, Literal: ""}, nil
	}

	ch := l.input[l.pos]

	// 数值字面量（含时长，如 5m）
	if unicode.IsDigit(ch) {
		return l.readNumber()
	}

	// 双引号字符串
	if ch == '"' {
		return l.readString()
	}

	// 标识符（函数名、指标名、标签键、"by" 关键字）
	if unicode.IsLetter(ch) || ch == '_' {
		return l.readIdent()
	}

	// 单字符或双字符运算符
	l.pos++
	switch ch {
	case '(':
		return Token{Type: TOK_LPAREN, Literal: "("}, nil
	case ')':
		return Token{Type: TOK_RPAREN, Literal: ")"}, nil
	case '{':
		return Token{Type: TOK_LBRACE, Literal: "{"}, nil
	case '}':
		return Token{Type: TOK_RBRACE, Literal: "}"}, nil
	case '[':
		return Token{Type: TOK_LBRACKET, Literal: "["}, nil
	case ']':
		return Token{Type: TOK_RBRACKET, Literal: "]"}, nil
	case ',':
		return Token{Type: TOK_COMMA, Literal: ","}, nil
	case '+':
		return Token{Type: TOK_PLUS, Literal: "+"}, nil
	case '-':
		return Token{Type: TOK_MINUS, Literal: "-"}, nil
	case '*':
		return Token{Type: TOK_STAR, Literal: "*"}, nil
	case '/':
		return Token{Type: TOK_SLASH, Literal: "/"}, nil
	case '=':
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TOK_EQEQ, Literal: "=="}, nil
		}
		return Token{Type: TOK_EQ, Literal: "="}, nil
	case '!':
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TOK_NEQ, Literal: "!="}, nil
		}
		return Token{}, fmt.Errorf("词法错误：'!' 后应跟 '='，位置 %d", l.pos)
	case '>':
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TOK_GTE, Literal: ">="}, nil
		}
		return Token{Type: TOK_GT, Literal: ">"}, nil
	case '<':
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TOK_LTE, Literal: "<="}, nil
		}
		return Token{Type: TOK_LT, Literal: "<"}, nil
	}
	return Token{}, fmt.Errorf("词法错误：未知字符 %q，位置 %d", ch, l.pos-1)
}

// readNumber 读取数值；若直接跟随时间单位字母（m/h/d/s），则识别为时长
func (l *Lexer) readNumber() (Token, error) {
	start := l.pos
	for l.pos < len(l.input) && (unicode.IsDigit(l.input[l.pos]) || l.input[l.pos] == '.') {
		l.pos++
	}
	lit := string(l.input[start:l.pos])

	// 检查是否紧跟时间单位（且后面没有更多字母，以免把 "5min" 中的 "in" 也吃掉）
	if l.pos < len(l.input) {
		unit := l.input[l.pos]
		if unit == 'm' || unit == 'h' || unit == 'd' || unit == 's' {
			nextPos := l.pos + 1
			if nextPos >= len(l.input) || !unicode.IsLetter(l.input[nextPos]) {
				l.pos++
				return Token{Type: TOK_DURATION, Literal: lit + string(unit)}, nil
			}
		}
	}
	return Token{Type: TOK_NUMBER, Literal: lit}, nil
}

// readString 读取双引号包围的字符串
func (l *Lexer) readString() (Token, error) {
	l.pos++ // 跳过开头 "
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != '"' {
		l.pos++
	}
	if l.pos >= len(l.input) {
		return Token{}, fmt.Errorf("词法错误：字符串未闭合")
	}
	s := string(l.input[start:l.pos])
	l.pos++ // 跳过结尾 "
	return Token{Type: TOK_STRING, Literal: s}, nil
}

// readIdent 读取标识符（字母、数字、下划线组成）
func (l *Lexer) readIdent() (Token, error) {
	start := l.pos
	for l.pos < len(l.input) && (unicode.IsLetter(l.input[l.pos]) ||
		unicode.IsDigit(l.input[l.pos]) || l.input[l.pos] == '_') {
		l.pos++
	}
	return Token{Type: TOK_IDENT, Literal: string(l.input[start:l.pos])}, nil
}
