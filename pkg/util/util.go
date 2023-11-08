package util

func Reverse(slice []interface{}) (out []interface{}) {
	for i := len(slice) - 1; i >= 0; i-- {
		out = append(out, slice[i])
	}
	return out
}
