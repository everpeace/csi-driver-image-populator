package image

import (
	"fmt"
	"time"
)

type tagGeneratorType string

var (
	tagGeneratorTypeTimestamp tagGeneratorType = "timestamp"
	tagGeneratorTypeFixed     tagGeneratorType = "fixed"
)

type tagGenerator struct {
	generatorType tagGeneratorType
	fixedTag      string
}

func (tg *tagGenerator) generate() string {
	switch tg.generatorType {
	case tagGeneratorTypeFixed:
		return tg.fixedTag
	case tagGeneratorTypeTimestamp:
		return fmt.Sprintf("%d", time.Now().UTC().Unix())
	default:
		return ""
	}
}
