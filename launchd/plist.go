// Package launchd holds Postbode's LaunchAgent artifact
// (be.vhco.postbode.plist, F-61) and a minimal parser used only by this
// package's own test to assert on that file's structure (AC-25's
// structural half — see plist_test.go's doc comment on what is and is not
// machine-verifiable without installing to a real machine).
package launchd

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
)

// plistDict is the raw XML shape of a top-level <plist><dict>...</dict>,
// covering exactly the value kinds be.vhco.postbode.plist uses: <string>,
// <true/>/<false/>, and <array> of <string>. Apple's plist DTD allows far
// more (dict nesting, integers, dates, data); this parser deliberately
// does not — it exists to validate one specific file, not to be a general
// plist library (NF-01/NF-13: no new dependency for this).
type plistDict struct {
	XMLName xml.Name    `xml:"plist"`
	Dict    rawDictBody `xml:"dict"`
}

type rawDictBody struct {
	Items []rawDictItem `xml:",any"`
}

type rawDictItem struct {
	XMLName xml.Name
	Content []byte `xml:",innerxml"`
}

// Value is one parsed <dict> entry's value: exactly one of Str, Bool (with
// BoolSet true) or Array is meaningful, mirroring the XML kinds this
// parser supports.
type Value struct {
	Str     string
	BoolVal bool
	BoolSet bool
	Array   []string
}

// Parse reads and structurally parses the plist at path into an ordered
// key/value map (Go maps are unordered, but launchd itself does not care
// about key order — this parser doesn't preserve it either).
func Parse(path string) (map[string]Value, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("launchd: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ParseReader(f)
}

// ParseReader is Parse over an already-open io.Reader.
func ParseReader(r io.Reader) (map[string]Value, error) {
	var doc plistDict
	dec := xml.NewDecoder(r)
	dec.Strict = false
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("launchd: decode plist xml: %w", err)
	}

	out := make(map[string]Value)
	var pendingKey string
	for _, item := range doc.Dict.Items {
		switch item.XMLName.Local {
		case "key":
			pendingKey = string(item.Content)
		case "string":
			if pendingKey == "" {
				continue
			}
			out[pendingKey] = Value{Str: string(item.Content)}
			pendingKey = ""
		case "true", "false":
			if pendingKey == "" {
				continue
			}
			out[pendingKey] = Value{BoolVal: item.XMLName.Local == "true", BoolSet: true}
			pendingKey = ""
		case "array":
			if pendingKey == "" {
				continue
			}
			arr, err := parseStringArray(item.Content)
			if err != nil {
				return nil, err
			}
			out[pendingKey] = Value{Array: arr}
			pendingKey = ""
		}
	}
	return out, nil
}

func parseStringArray(inner []byte) ([]string, error) {
	type arrayBody struct {
		Strings []string `xml:"string"`
	}
	var body arrayBody
	wrapped := append([]byte("<array>"), append(inner, []byte("</array>")...)...)
	if err := xml.Unmarshal(wrapped, &body); err != nil {
		return nil, fmt.Errorf("launchd: decode plist array: %w", err)
	}
	return body.Strings, nil
}
