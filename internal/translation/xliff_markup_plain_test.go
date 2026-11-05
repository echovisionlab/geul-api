package translation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXLIFFTargetFromPlainTextKeepsTheSourceLinkAroundOneRun(t *testing.T) {
	for name, test := range map[string]struct {
		source, target, want string
		rejected             bool
	}{
		"no codes":        {source: "제품", target: "Products", want: "Products"},
		"text is escaped": {source: "제품", target: "A & <B>", want: "A &amp; &lt;B&gt;"},
		"a linked word": {
			source: `<pc id="l1"><pc id="r1">제품 소개</pc></pc>`, target: "Overview",
			want: `<pc id="l1"><pc id="r1">Overview</pc></pc>`,
		},
		"an empty source": {source: "", target: "Caption", want: "Caption"},
		"markup passes through": {
			source: `<pc id="r1">앞</pc><pc id="r2">뒤</pc>`, target: `<pc id="r1">A</pc><pc id="r2">B</pc>`,
			want: `<pc id="r1">A</pc><pc id="r2">B</pc>`,
		},
		"plain text for several runs": {source: `<pc id="r1">앞</pc><pc id="r2">뒤</pc>`, target: "AB", rejected: true},
		"plain text around a break":   {source: `<pc id="r1">앞</pc><ph id="h1" equiv="` + "\n" + `"/>`, target: "AB", rejected: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := XLIFFTargetFromPlainText(test.source, test.target)
			if test.rejected {
				require.ErrorContains(t, err, test.source, "the error must show the codes to keep")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestXLIFFMarkupPlainTextReadsPlaceholdersAsTheirText(t *testing.T) {
	markup := `<pc id="r1">A &amp; B</pc><ph id="h1" equiv="` + "\n" + `"/><ph id="h2"/><pc id="l1"><pc id="r2">C</pc></pc>`

	require.Equal(t, "A & B\nC", XLIFFMarkupPlainText(markup))
}

func TestXLIFFSimplifiedMarkupOnlySimplifiesOneRun(t *testing.T) {
	require.Equal(t, "제품 소개", XLIFFSimplifiedMarkup(`<pc id="l1"><pc id="r1">제품 소개</pc></pc>`))
	several := `<pc id="r1">앞</pc><pc id="r2">뒤</pc>`
	require.Equal(t, several, XLIFFSimplifiedMarkup(several))
}
