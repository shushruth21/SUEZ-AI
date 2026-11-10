package main

import (
	"regexp"
	"strings"
)

// secretPatterns find likely credentials in text before it is stored. This is
// a best-effort guard, not a guarantee: it catches common key formats and
// obvious assignments, and anything it finds is replaced, never stored.
var secretPatterns = []struct {
	label   string
	pattern *regexp.Regexp
}{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)},
	{"suez-key", regexp.MustCompile(`\bsk_suez_[A-Za-z0-9]{16,}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`)},
	{"api-key", regexp.MustCompile(`\b(sk|rk|pk)-[A-Za-z0-9_-]{20,}`)},
	{"groq-key", regexp.MustCompile(`\bgsk_[A-Za-z0-9]{20,}\b`)},
	{"github-token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{30,}\b`)},
	{"aws-access-key", regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"google-key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}\b`)},
	{"bearer-token", regexp.MustCompile(`(?i)\b(bearer|apikey)\s+[A-Za-z0-9._~+/-]{16,}=*`)},
	{"connection-string", regexp.MustCompile(`(?i)\b(postgres(ql)?|mysql|mongodb(\+srv)?|redis|amqp)://[^\s:/@]+:[^\s@/]+@[^\s]+`)},
	{"credential", regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret)\b["']?\s*[:=]\s*["']?[^\s"',;]{6,}`)},
}

// redactSecrets returns text with likely credentials replaced and how many
// replacements were made.
func redactSecrets(text string) (string, int) {
	count := 0
	for _, entry := range secretPatterns {
		text = entry.pattern.ReplaceAllStringFunc(text, func(string) string {
			count++
			return "[REDACTED:" + entry.label + "]"
		})
	}
	return text, count
}

func previewOf(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return text[:cut] + "…"
}

func estimateTokens(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	return (len(trimmed) + 3) / 4
}
