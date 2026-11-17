package gateway

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// limitResult caps the serialised size of an upstream result, keeping content
// types intact where possible and marking whatever was dropped.
func limitResult(res *mcp.CallToolResult) *mcp.CallToolResult {
	type item struct {
		content mcp.Content
		size    int
	}
	items := make([]item, 0, len(res.Content))
	total := 0
	for _, c := range res.Content {
		b, err := json.Marshal(c)
		if err != nil {
			continue
		}
		items = append(items, item{c, len(b)})
		total += len(b)
	}
	if total <= resultBudget {
		return res
	}

	kept := make([]mcp.Content, 0, len(items))
	used := 0
	for _, it := range items {
		if used+it.size <= resultBudget {
			kept = append(kept, it.content)
			used += it.size
			continue
		}
		if tc, ok := it.content.(*mcp.TextContent); ok {
			room := resultBudget - used - 128 // leave space for the marker
			if room > 0 {
				kept = append(kept, &mcp.TextContent{Text: cutBytes(tc.Text, room)})
			}
		}
		break
	}
	kept = append(kept, &mcp.TextContent{
		Text: fmt.Sprintf("… (truncated by gateway, %d bytes total)", total),
	})
	res.Content = kept
	return res
}

// cutBytes bounds a string by bytes without splitting a UTF-8 rune.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s[:n])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
