package response

import (
	"reflect"
	"strings"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// init makes validation errors name a field the way the client wrote it in JSON ("price"), not the way the Go
// struct names it ("Price"). It must run before the first validation, so it is done at start-up.
func init() {
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterTagNameFunc(jsonFieldName)
	}
}

// jsonFieldName returns the name in the `json` tag; "" (use the Go name) when there is none or it is "-".
func jsonFieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}
