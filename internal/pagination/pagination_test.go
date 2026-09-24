package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type key struct {
	CreatedAt time.Time `json:"c"`
	ID        int       `json:"i"`
}

func TestParseAndDecode(t *testing.T) {
	p, err := Parse(httptest.NewRequest(http.MethodGet, "/?limit=10", nil))
	require.NoError(t, err)
	assert.Equal(t, 10, p.Limit)
	var k key
	has, err := p.Decode(&k)
	require.NoError(t, err)
	assert.False(t, has)

	for _, q := range []string{"?limit=0", "?limit=201", "?limit=x"} {
		_, err := Parse(httptest.NewRequest(http.MethodGet, "/"+q, nil))
		assert.Error(t, err, q)
	}
	p, _ = Parse(httptest.NewRequest(http.MethodGet, "/?cursor=not-base64!!", nil))
	_, err = p.Decode(&k)
	assert.Error(t, err)
}

func TestBuildRoundTrip(t *testing.T) {
	rows := []int{5, 4, 3}
	page := Build(rows, 2, func(r int) int { return r * 10 }, func(r int) any { return key{ID: r} })
	assert.Equal(t, []int{50, 40}, page.Items)
	require.NotNil(t, page.NextCursor)

	p, _ := Parse(httptest.NewRequest(http.MethodGet, "/?cursor="+*page.NextCursor, nil))
	var k key
	has, err := p.Decode(&k)
	require.NoError(t, err)
	assert.True(t, has)
	assert.Equal(t, 4, k.ID, "cursor points at the last returned row")

	last := Build(rows, 5, func(r int) int { return r }, func(r int) any { return r })
	assert.Nil(t, last.NextCursor)
	assert.Len(t, last.Items, 3)

	empty := Build([]int{}, 5, func(r int) int { return r }, func(r int) any { return r })
	assert.NotNil(t, empty.Items, "items is [] not null")
}
