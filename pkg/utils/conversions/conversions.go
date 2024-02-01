package conversions

// before golang supports the conversion natively, we use this function to convert bool to int.
// tracking golang support at https://github.com/golang/go/issues/64825
func BoolToint(b bool) int {
	if b {
		return 1
	}
	return 0
}

func IntToBool(i int) bool {
	return i == 1
}
