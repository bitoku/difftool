package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cockroachdb/errors"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/yaml"

	"difftool/pkg/objdiff"
)

// if we unmarshall yaml directly, int64 is inferred as float64 somehow,
// so we convert yaml to json first and then unmarshall it
func unmarshall(data []byte, v any) error {
	jsonContent, err := yaml.ToJSON(data)
	if err != nil {
		return errors.WithStack(err)
	}
	err = json.Unmarshal(jsonContent, v)
	return errors.WithStack(err)
}

func loadYaml(path string, v any) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return errors.WithStack(err)
	}
	return unmarshall(file, v)
}

func main() {
	//diffOpts := []cmp.Option{objdiff.IgnoreMapEntries(target.Ignore)}
	var obj1, obj2 objdiff.Object
	err := loadYaml(os.Args[1], &obj1)
	if err != nil {
		panic(err.Error())
	}
	err = loadYaml(os.Args[2], &obj2)
	if err != nil {
		panic(err.Error())
	}
	if obj1.IsList() {

	}
	fmt.Println(cmp.Diff(obj1.Spec, obj2.Spec))
}
