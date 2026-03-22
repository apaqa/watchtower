// Package wql - 词法分析器测试
package wql

import (
	"testing"
)

// TestLexerSimpleFunction 验证基本函数调用（无标签、含时间范围）的分词结果
func TestLexerSimpleFunction(t *testing.T) {
	tokens, err := NewLexer("avg(cpu_usage_percent[5m])").Tokenize()
	if err != nil {
		t.Fatalf("词法分析失败: %v", err)
	}

	// 期望的词法单元序列（不含 EOF）
	want := []struct {
		typ     TokenType
		literal string
	}{
		{TOK_IDENT, "avg"},
		{TOK_LPAREN, "("},
		{TOK_IDENT, "cpu_usage_percent"},
		{TOK_LBRACKET, "["},
		{TOK_DURATION, "5m"},
		{TOK_RBRACKET, "]"},
		{TOK_RPAREN, ")"},
		{TOK_EOF, ""},
	}

	if len(tokens) != len(want) {
		t.Fatalf("Token 数量：期望 %d，实际 %d\n tokens=%v", len(want), len(tokens), tokens)
	}
	for i, w := range want {
		if tokens[i].Type != w.typ || tokens[i].Literal != w.literal {
			t.Errorf("token[%d]: 期望 (%d,%q)，实际 (%d,%q)", i, w.typ, w.literal, tokens[i].Type, tokens[i].Literal)
		}
	}
}

// TestLexerWithLabels 验证带标签过滤器的函数调用分词
func TestLexerWithLabels(t *testing.T) {
	tokens, err := NewLexer(`avg(cpu_usage_percent{host="DESKTOP"}[5m])`).Tokenize()
	if err != nil {
		t.Fatalf("词法分析失败: %v", err)
	}

	want := []struct {
		typ     TokenType
		literal string
	}{
		{TOK_IDENT, "avg"},
		{TOK_LPAREN, "("},
		{TOK_IDENT, "cpu_usage_percent"},
		{TOK_LBRACE, "{"},
		{TOK_IDENT, "host"},
		{TOK_EQ, "="},
		{TOK_STRING, "DESKTOP"},
		{TOK_RBRACE, "}"},
		{TOK_LBRACKET, "["},
		{TOK_DURATION, "5m"},
		{TOK_RBRACKET, "]"},
		{TOK_RPAREN, ")"},
		{TOK_EOF, ""},
	}

	if len(tokens) != len(want) {
		t.Fatalf("Token 数量：期望 %d，实际 %d", len(want), len(tokens))
	}
	for i, w := range want {
		if tokens[i].Type != w.typ || tokens[i].Literal != w.literal {
			t.Errorf("token[%d]: 期望 (%d,%q)，实际 (%d,%q)", i, w.typ, w.literal, tokens[i].Type, tokens[i].Literal)
		}
	}
}

// TestLexerArithmetic 验证算术表达式分词
func TestLexerArithmetic(t *testing.T) {
	tokens, err := NewLexer("metric_a / metric_b * 100").Tokenize()
	if err != nil {
		t.Fatalf("词法分析失败: %v", err)
	}

	wantTypes := []TokenType{
		TOK_IDENT, TOK_SLASH, TOK_IDENT, TOK_STAR, TOK_NUMBER, TOK_EOF,
	}
	wantLits := []string{"metric_a", "/", "metric_b", "*", "100", ""}

	if len(tokens) != len(wantTypes) {
		t.Fatalf("Token 数量：期望 %d，实际 %d", len(wantTypes), len(tokens))
	}
	for i := range wantTypes {
		if tokens[i].Type != wantTypes[i] {
			t.Errorf("token[%d] type: 期望 %d，实际 %d", i, wantTypes[i], tokens[i].Type)
		}
		if tokens[i].Literal != wantLits[i] {
			t.Errorf("token[%d] lit: 期望 %q，实际 %q", i, wantLits[i], tokens[i].Literal)
		}
	}
}

// TestLexerComparison 验证比较运算符分词
func TestLexerComparison(t *testing.T) {
	tokens, err := NewLexer("avg(cpu_usage_percent[5m]) > 80").Tokenize()
	if err != nil {
		t.Fatalf("词法分析失败: %v", err)
	}

	// 找到 GT 和数字 80
	var foundGT, found80 bool
	for _, tok := range tokens {
		if tok.Type == TOK_GT {
			foundGT = true
		}
		if tok.Type == TOK_NUMBER && tok.Literal == "80" {
			found80 = true
		}
	}
	if !foundGT {
		t.Error("未找到 '>' Token")
	}
	if !found80 {
		t.Error("未找到数字 '80' Token")
	}
}

// TestLexerDurations 验证各种时长格式（1m, 15m, 1h, 1d）均被识别为 DURATION
func TestLexerDurations(t *testing.T) {
	cases := []string{"1m", "15m", "1h", "1d"}
	for _, dur := range cases {
		tokens, err := NewLexer("[" + dur + "]").Tokenize()
		if err != nil {
			t.Fatalf("[%s] 词法分析失败: %v", dur, err)
		}
		found := false
		for _, tok := range tokens {
			if tok.Type == TOK_DURATION && tok.Literal == dur {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("时长 %q 未被识别为 DURATION Token", dur)
		}
	}
}

// TestLexerMultipleLabels 验证多标签分词正确包含逗号
func TestLexerMultipleLabels(t *testing.T) {
	tokens, err := NewLexer(`metric{host="A",env="prod"}`).Tokenize()
	if err != nil {
		t.Fatalf("词法分析失败: %v", err)
	}
	commaCount := 0
	for _, tok := range tokens {
		if tok.Type == TOK_COMMA {
			commaCount++
		}
	}
	if commaCount != 1 {
		t.Errorf("期望 1 个逗号，实际 %d 个", commaCount)
	}
}
