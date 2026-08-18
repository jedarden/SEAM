package spec

import "github.com/pb33f/libopenapi/datamodel/high/v3"

// OpenAPIModel returns the loaded high-level model for consumers that build a
// runtime route table. The model is immutable after Loader construction.
func (l *Loader) OpenAPIModel() *v3.Document {
	if l == nil || l.model == nil {
		return nil
	}
	return &l.model.Model
}
