package reactions

import (
	_ "embed"
	"strings"
)

//go:embed emoji.txt
var catalogue string

var allowed = func() map[string]bool {
	result := map[string]bool{}
	for _, emoji := range strings.Fields(catalogue) {
		result[emoji] = true
	}
	return result
}()

func Valid(emoji string) bool { return allowed[emoji] }
