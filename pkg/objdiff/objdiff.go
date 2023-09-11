package objdiff

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/strings/slices"
)

type Object struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata"`
	Spec          any       `json:"spec,omitempty"`
	Data          any       `json:"data,omitempty"`
	Items         []*Object `json:"items,omitempty"`
}

func (o *Object) IsList() bool {
	return o.Items != nil
}

func (o *Object) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s %s %s", o.APIVersion, o.Kind, o.Name)
	}
	return fmt.Sprintf("%s %s %s/%s", o.APIVersion, o.Kind, o.Namespace, o.Name)
}

func IgnoreMapEntries(ignoredKeys []string) cmp.Option {
	filter := func(path cmp.Path) bool {
		var key []string
		for _, ps := range path {
			switch x := ps.(type) {
			case cmp.MapIndex:
				key = append(key, x.Key().String())
			case cmp.SliceIndex:
				key = append(key, strconv.Itoa(x.Key()))
			}
		}
		// check it naively since ignoredKeys won't be so long,
		return slices.Contains(ignoredKeys, strings.Join(key, "."))
	}
	return cmp.FilterPath(filter, cmp.Ignore())
}

func compare(a, b *Object, opts ...cmp.Option) string {
	return cmp.Diff(a.Spec, b.Spec, opts...)
}

func unmarshallUnstructured(u *unstructured.Unstructured, v any) error {
	rawJson, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(rawJson, v)
}

func CheckObj(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource, opts ...cmp.Option) (presences, diffs []string, err error) {
	// get the current manifest
	resp, err := client.
		Resource(resource).
		Get(context.Background(), obj.Name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		return []string{fmt.Sprintf("- %s is not found\n", obj)}, []string{}, nil
	}
	if err != nil {
		return []string{}, []string{}, err
	}

	curr := new(Object)
	err = unmarshallUnstructured(resp, curr)
	if err != nil {
		return []string{}, []string{}, err
	}
	diff := compare(obj, curr, opts...)
	if diff != "" {
		diffs = append(diffs, diff)
	}
	return []string{}, diffs, nil
}

func CheckList(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource, opts ...cmp.Option) (presences, diffs []string, err error) {
	resp, err := client.
		Resource(resource).
		List(context.Background(), v1.ListOptions{})
	if err != nil {
		return []string{}, []string{}, err
	}

	m := make(map[string]*Object)
	checked := make(map[string]bool)
	for _, i := range resp.Items {
		curr := new(Object)
		err = unmarshallUnstructured(&i, curr)
		if err != nil {
			return []string{}, []string{}, err
		}
		m[curr.String()] = curr
		checked[curr.String()] = false
	}
	for _, i := range obj.Items {
		curr, ok := m[i.String()]
		if !ok {
			presences = append(presences, fmt.Sprintf("- %s is not found\n", i))
			continue
		}
		checked[curr.String()] = true
		diff := compare(i, curr, opts...)
		if diff == "" {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s\n%s", i.String(), diff))
	}
	for k, v := range m {
		if !checked[k] {
			presences = append(presences, fmt.Sprintf("+ %s is found, but not in default\n", v))
		}
	}
	return presences, diffs, nil
}
