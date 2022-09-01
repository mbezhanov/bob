package gen

import (
	"strings"

	"github.com/stephenafamo/bob/orm/gen/drivers"
	"github.com/volatiletech/strmangle"
)

func relAlias(t drivers.Table) map[string]string {
	aliases := map[string]string{}

	for _, rel := range t.Relationships {
		// When not a direct relationship we just use the table name
		if len(rel.Sides) > 1 {
			aliases[rel.Name] = formatRelAlias(rel.Type, rel.Sides[len(rel.Sides)-1].To)
			continue
		}

		side := rel.Sides[0]

		// Just cop out and use the table name if there are multiple colummns
		if len(rel.Sides[0].Pairs) > 1 {
			aliases[rel.Name] = formatRelAlias(rel.Type, side.To)
			continue
		}
		var lcol, fcol string
		for l, f := range side.Pairs {
			lcol = l
			fcol = f
		}

		lcolTrimmed := strmangle.Singular(trimSuffixes(lcol))
		fcolTrimmed := strmangle.Singular(trimSuffixes(fcol))

		singularLocalTable := strmangle.Singular(side.From)
		singularForeignTable := strmangle.Singular(side.To)

		if lcolTrimmed == singularForeignTable || fcolTrimmed == singularLocalTable {
			aliases[rel.Name] = formatRelAlias(rel.Type, side.To)
			continue
		}

		// Just use the longer column name
		// Anything more specific and the user should just set it up
		colToUse := lcolTrimmed
		if len(fcolTrimmed) > len(lcolTrimmed) {
			colToUse = fcolTrimmed
		}

		if side.To == side.From {
			// Handle special case of self-join
			aliases[rel.Name] = formatRelAlias(rel.Type, colToUse)
			continue
		}

		aliases[rel.Name] = formatRelAlias(rel.Type, colToUse+"_"+side.To)
	}

	return aliases
}

//nolint:gochecknoglobals
var identifierSuffixes = []string{"_id", "_uuid", "_guid", "_oid"}

// trimSuffixes from the identifier
func trimSuffixes(str string) string {
	ln := len(str)
	for _, s := range identifierSuffixes {
		str = strings.TrimSuffix(str, s)
		if len(str) != ln {
			break
		}
	}

	return str
}

func formatRelAlias(relType drivers.RelType, name string) string {
	if relType == drivers.ToMany {
		return strmangle.TitleCase(strmangle.Plural(name))
	}

	return strmangle.TitleCase(strmangle.Singular(name))
}
