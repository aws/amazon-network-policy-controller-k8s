package conversions

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBoolToIntConversion(t *testing.T) {
	assert.Equal(t, 1, BoolToint(true))
	assert.Equal(t, 0, BoolToint(false))
}

func TestIntToBoolConversion(t *testing.T) {
	assert.Equal(t, false, IntToBool(0))
	assert.Equal(t, true, IntToBool(1))
	assert.Equal(t, false, IntToBool(2))
}
