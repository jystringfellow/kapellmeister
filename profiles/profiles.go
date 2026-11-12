package profiles

import "embed"

//go:embed km-*.md
var Files embed.FS

func Source(name string) ([]byte, error) {
	return Files.ReadFile(name + ".md")
}
