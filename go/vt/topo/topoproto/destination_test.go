/*
Copyright 2019 The Vitess Authors.

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

package topoproto

import (
	"encoding/hex"
	"reflect"
	"testing"

	"vitess.io/vitess/go/vt/key"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

func TestParseDestination(t *testing.T) {
	tenHexBytes, _ := hex.DecodeString("10")
	twentyHexBytes, _ := hex.DecodeString("20")

	testcases := []struct {
		targetString string
		dest         key.ShardDestination
		keyspace     string
		tabletType   topodatapb.TabletType
	}{{
		targetString: "ks[10-20]@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationExactKeyRange{KeyRange: &topodatapb.KeyRange{Start: tenHexBytes, End: twentyHexBytes}},
	}, {
		targetString: "ks[-]@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationExactKeyRange{KeyRange: &topodatapb.KeyRange{}},
	}, {
		targetString: "ks[deadbeef]@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationKeyspaceID([]byte("\xde\xad\xbe\xef")),
	}, {
		targetString: "ks[10-]@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationExactKeyRange{KeyRange: &topodatapb.KeyRange{Start: tenHexBytes}},
	}, {
		targetString: "ks[-20]@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationExactKeyRange{KeyRange: &topodatapb.KeyRange{End: twentyHexBytes}},
	}, {
		targetString: "ks:-80@primary",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationShard("-80"),
	}, {
		targetString: ":-80@primary",
		keyspace:     "",
		tabletType:   topodatapb.TabletType_PRIMARY,
		dest:         key.DestinationShard("-80"),
	}, {
		targetString: "@primary",
		keyspace:     "",
		tabletType:   topodatapb.TabletType_PRIMARY,
	}, {
		targetString: "@replica",
		keyspace:     "",
		tabletType:   topodatapb.TabletType_REPLICA,
	}, {
		targetString: "ks",
		keyspace:     "ks",
		tabletType:   topodatapb.TabletType_PRIMARY,
	}, {
		targetString: "ks/-80",
		keyspace:     "ks",
		dest:         key.DestinationShard("-80"),
		tabletType:   topodatapb.TabletType_PRIMARY,
	}}

	for _, tcase := range testcases {
		if targetKeyspace, targetTabletType, targetDest, _ := ParseDestination(tcase.targetString, topodatapb.TabletType_PRIMARY); !reflect.DeepEqual(targetDest, tcase.dest) || targetKeyspace != tcase.keyspace || targetTabletType != tcase.tabletType {
			t.Errorf("ParseDestination(%s) - got: (%v, %v, %v), want (%v, %v, %v)",
				tcase.targetString,
				targetDest,
				targetKeyspace,
				targetTabletType,
				tcase.dest,
				tcase.keyspace,
				tcase.tabletType,
			)
		}
	}

	_, _, _, err := ParseDestination("ks[20-40-60]", topodatapb.TabletType_PRIMARY)
	want := "single keyrange expected in 20-40-60"
	if err == nil || err.Error() != want {
		t.Errorf("executorExec error: %v, want %s", err, want)
	}

	_, _, _, err = ParseDestination("ks[--60]", topodatapb.TabletType_PRIMARY)
	want = "malformed spec: MinKey/MaxKey cannot be in the middle of the spec: \"--60\""
	if err == nil || err.Error() != want {
		t.Errorf("executorExec error: %v, want %s", err, want)
	}

	_, _, _, err = ParseDestination("ks[qrnqorrs]@primary", topodatapb.TabletType_PRIMARY)
	want = "expected valid hex in keyspace id qrnqorrs"
	if err == nil || err.Error() != want {
		t.Errorf("executorExec error: %v, want %s", err, want)
	}
}
