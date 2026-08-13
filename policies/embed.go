package policyassets

import "embed"

type Asset struct {
	Name     string
	Markdown []byte
}

//go:embed customer-care-v1.md damaged-goods-v3.md refunds-v2.md
var embeddedPolicies embed.FS

var policyNames = []string{
	"customer-care-v1.md",
	"damaged-goods-v3.md",
	"refunds-v2.md",
}

func All() []Asset {
	assets := make([]Asset, len(policyNames))
	for index, name := range policyNames {
		markdown, err := embeddedPolicies.ReadFile(name)
		if err != nil {
			panic("embedded policy asset is unavailable: " + name)
		}
		assets[index] = Asset{Name: name, Markdown: append([]byte(nil), markdown...)}
	}
	return assets
}
