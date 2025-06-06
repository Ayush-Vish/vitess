/*
Copyright 2021 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package collations

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

type colldefaults struct {
	Default ID
	Binary  ID
}

// Environment is a collation environment for a MySQL version, which contains
// a database of collations and defaults for that specific version.
type Environment struct {
	version       collver
	byName        map[string]ID
	byCharset     map[string]*colldefaults
	byCharsetName map[ID]string
	unsupported   map[string]ID
	byID          map[ID]string
}

// LookupByName returns the collation with the given name.
func (env *Environment) LookupByName(name string) ID {
	return env.byName[name]
}

// LookupID returns the collation ID for the given name, and whether
// the collation is supported by this package.
func (env *Environment) LookupID(name string) (ID, bool) {
	if supported, ok := env.byName[name]; ok {
		return supported, true
	}
	if unsup, ok := env.unsupported[name]; ok {
		return unsup, false
	}
	return Unknown, false
}

// LookupName returns the collation name for the given ID and whether
// the collation is supported by this package.
func (env *Environment) LookupName(id ID) string {
	return env.byID[id]
}

// DefaultCollationForCharset returns the default collation for a charset
func (env *Environment) DefaultCollationForCharset(charset string) ID {
	if defaults, ok := env.byCharset[charset]; ok {
		return defaults.Default
	}
	return Unknown
}

// BinaryCollationForCharset returns the default binary collation for a charset
func (env *Environment) BinaryCollationForCharset(charset string) ID {
	if defaults, ok := env.byCharset[charset]; ok {
		return defaults.Binary
	}
	return Unknown
}

var globalEnvironments = make(map[collver]*Environment)
var globalEnvironmentsMu sync.Mutex

// fetchCacheEnvironment returns a cached Environment from a global cache.
// We can keep a single Environment per collver version because Environment
// objects are immutable once constructed.
func fetchCacheEnvironment(version collver) *Environment {
	globalEnvironmentsMu.Lock()
	defer globalEnvironmentsMu.Unlock()

	var env *Environment
	if env = globalEnvironments[version]; env == nil {
		env = makeEnv(version)
		globalEnvironments[version] = env
	}
	return env
}

// NewEnvironment creates a collation Environment for the given MySQL version string.
// The version string must be in the format that is sent by the server as the version packet
// when opening a new MySQL connection
func NewEnvironment(serverVersion string) *Environment {
	// 8.0 is the oldest fully supported version, so use that as the default.
	// All newer MySQL versions including 9 are so far compatible as well.
	var version = collverMySQL8
	serverVersion = strings.TrimSpace(strings.ToLower(serverVersion))
	switch {
	case strings.Contains(serverVersion, "mariadb"):
		switch {
		case strings.Contains(serverVersion, "10.0."):
			version = collverMariaDB100
		case strings.Contains(serverVersion, "10.1."):
			version = collverMariaDB101
		case strings.Contains(serverVersion, "10.2."):
			version = collverMariaDB102
		case strings.Contains(serverVersion, "10.3."):
			version = collverMariaDB103
		}
	case strings.HasPrefix(serverVersion, "5.6."):
		version = collverMySQL56
	case strings.HasPrefix(serverVersion, "5.7."):
		version = collverMySQL57
	}
	return fetchCacheEnvironment(version)
}

func makeEnv(version collver) *Environment {
	env := &Environment{
		version:       version,
		byName:        make(map[string]ID),
		byCharset:     make(map[string]*colldefaults),
		byCharsetName: make(map[ID]string),
		byID:          make(map[ID]string),
		unsupported:   make(map[string]ID),
	}

	for collid, vi := range globalVersionInfo {
		var ournames []string
		var ourcharsets []string
		for _, alias := range vi.alias {
			if alias.mask&version != 0 {
				ournames = append(ournames, alias.name)
				ourcharsets = append(ourcharsets, alias.charset)
			}
		}
		if len(ournames) == 0 {
			continue
		}

		if int(collid) >= len(supported) || supported[collid] == "" {
			for _, name := range ournames {
				env.unsupported[name] = collid
			}
			continue
		}

		for i, name := range ournames {
			cs := ourcharsets[i]
			env.byName[name] = collid
			env.byID[collid] = name
			env.byCharsetName[collid] = cs
			defaults := env.byCharset[cs]
			if defaults == nil {
				defaults = &colldefaults{}
				env.byCharset[cs] = defaults
			}
			if vi.isdefault&version != 0 {
				defaults.Default = collid
			}
			if strings.HasSuffix(name, "_bin") && defaults.Binary < collid {
				defaults.Binary = collid
			}
		}
	}

	for from, to := range charsetAliases() {
		env.byCharset[from] = env.byCharset[to]
	}

	return env
}

// A few interesting character set values.
// See http://dev.mysql.com/doc/internals/en/character-set.html#packet-Protocol::CharacterSet
const (
	CollationUtf8mb3ID     = 33
	CollationUtf8mb4ID     = 255
	CollationBinaryID      = 63
	CollationUtf8mb4BinID  = 46
	CollationLatin1Swedish = 8
)

// SystemCollation is the default collation for the system tables
// such as the information schema. This is still utf8mb3 to match
// MySQLs behavior. This means that you can't use utf8mb4 in table
// names, column names, without running into significant issues.
var SystemCollation = TypedCollation{
	Collation:    CollationUtf8mb3ID,
	Coercibility: CoerceCoercible,
	Repertoire:   RepertoireUnicode,
}

// CharsetAlias returns the internal charset name for the given charset.
// For now, this only maps `utf8` to `utf8mb3`; in future versions of MySQL,
// this mapping will change, so it's important to use this helper so that
// Vitess code has a consistent mapping for the active collations environment.
func (env *Environment) CharsetAlias(charset string) (alias string, ok bool) {
	alias, ok = charsetAliases()[charset]
	return
}

// CollationAlias returns the internal collaction name for the given charset.
// For now, this maps all `utf8` to `utf8mb3` collation names; in future versions of MySQL,
// this mapping will change, so it's important to use this helper so that
// Vitess code has a consistent mapping for the active collations environment.
func (env *Environment) CollationAlias(collation string) (string, bool) {
	col := env.LookupByName(collation)
	if col == Unknown {
		return collation, false
	}
	allCols, ok := globalVersionInfo[col]
	if !ok {
		return collation, false
	}
	if len(allCols.alias) == 1 {
		return collation, false
	}
	for _, alias := range allCols.alias {
		for source, dest := range charsetAliases() {
			if strings.HasPrefix(collation, fmt.Sprintf("%s_", source)) &&
				strings.HasPrefix(alias.name, fmt.Sprintf("%s_", dest)) {
				return alias.name, true
			}
		}
	}
	return collation, false
}

// DefaultConnectionCharset is the default charset that Vitess will use when negotiating a
// charset in a MySQL connection handshake. Note that in this context, a 'charset' is equivalent
// to a Collation ID, with the exception that it can only fit in 1 byte.
// For MySQL 8.0+ environments, the default charset is `utf8mb4_0900_ai_ci`.
// For older MySQL environments, the default charset is `utf8mb4_general_ci`.
func (env *Environment) DefaultConnectionCharset() ID {
	switch env.version {
	case collverMySQL8:
		return CollationUtf8mb4ID
	default:
		return 45
	}
}

// ParseConnectionCharset parses the given charset name and returns its numerical
// identifier to be used in a MySQL connection handshake. The charset name can be:
// - the name of a character set, in which case the default collation ID for the
// character set is returned.
// - the name of a collation, in which case the ID for the collation is returned,
// UNLESS the collation itself has an ID greater than 255; such collations are not
// supported because they cannot be negotiated in a single byte in our connection
// handshake.
// - empty, in which case the default connection charset for this MySQL version
// is returned.
func (env *Environment) ParseConnectionCharset(csname string) (ID, error) {
	if csname == "" {
		return env.DefaultConnectionCharset(), nil
	}

	var collid ID = 0
	csname = strings.ToLower(csname)
	if defaults, ok := env.byCharset[csname]; ok {
		collid = defaults.Default
	} else if coll, ok := env.byName[csname]; ok {
		collid = coll
	}
	if collid == 0 || collid > 255 {
		return 0, fmt.Errorf("unsupported connection charset: %q", csname)
	}
	return collid, nil
}

func (env *Environment) AllCollationIDs() []ID {
	all := make([]ID, 0, len(env.byID))
	for v := range env.byID {
		all = append(all, v)
	}
	slices.Sort(all)
	return all
}

func (env *Environment) LookupByCharset(name string) *colldefaults {
	return env.byCharset[name]
}

func (env *Environment) LookupCharsetName(coll ID) string {
	return env.byCharsetName[coll]
}

func (env *Environment) IsSupported(coll ID) bool {
	_, supported := env.byID[coll]
	return supported
}
