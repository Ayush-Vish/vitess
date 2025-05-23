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

package endtoend

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
	vtenv "vitess.io/vitess/go/vt/env"
	"vitess.io/vitess/go/vt/mysqlctl"
	vttestpb "vitess.io/vitess/go/vt/proto/vttest"
	"vitess.io/vitess/go/vt/tlstest"
	"vitess.io/vitess/go/vt/vttest"
)

var (
	connParams mysql.ConnParams
)

// assertSQLError makes sure we get the right error.
func assertSQLError(t *testing.T, err error, code sqlerror.ErrorCode, sqlState string, subtext string, query string) {
	t.Helper()
	require.Error(t, err, "was expecting SQLError %v / %v / %v but got no error.", code, sqlState, subtext)

	serr, ok := err.(*sqlerror.SQLError)
	require.True(t, ok, "was expecting SQLError %v / %v / %v but got: %v", code, sqlState, subtext, err)
	require.Equal(t, code, serr.Num, "was expecting SQLError %v / %v / %v but got code %v", code, sqlState, subtext, serr.Num)
	require.Equal(t, sqlState, serr.State, "was expecting SQLError %v / %v / %v but got state %v", code, sqlState, subtext, serr.State)
	require.True(t, subtext == "" || strings.Contains(serr.Message, subtext), "was expecting SQLError %v / %v / %v but got message %v", code, sqlState, subtext, serr.Message)
	require.Equal(t, query, serr.Query, "was expecting SQLError %v / %v / %v with Query '%v' but got query '%v'", code, sqlState, subtext, query, serr.Query)

}

// runMysql forks a mysql command line process connecting to the provided server.
func runMysql(t *testing.T, params *mysql.ConnParams, command string) (string, bool) {
	dir, err := vtenv.VtMysqlRoot()
	require.NoError(t, err, "vtenv.VtMysqlRoot failed: %v", err)

	name, err := binaryPath(dir, "mysql")
	require.NoError(t, err, "binaryPath failed: %v", err)

	// The args contain '-v' 3 times, to switch to very verbose output.
	// In particular, it has the message:
	// Query OK, 1 row affected (0.00 sec)

	version, err := mysqlctl.GetVersionString()
	if err != nil {
		failVersionDetection(err)
	}
	f, v, err := mysqlctl.ParseVersionString(version)
	if err != nil {
		failVersionDetection(err)
	}

	t.Logf("Using flavor: %v, version: %v", f, v)

	args := []string{
		"-v", "-v", "-v",
	}
	args = append(args, "-e", command)
	if params.UnixSocket != "" {
		args = append(args, "-S", params.UnixSocket)
	} else {
		args = append(args,
			"-h", params.Host,
			"-P", fmt.Sprintf("%v", params.Port))
	}
	if params.Uname != "" {
		args = append(args, "-u", params.Uname)
	}
	if params.Pass != "" {
		args = append(args, "-p"+params.Pass)
	}
	if params.DbName != "" {
		args = append(args, "-D", params.DbName)
	}
	if params.SslEnabled() {
		if f == mysqlctl.FlavorMySQL && v.Major == 5 && v.Minor == 7 || v.Major == 8 {
			args = append(args,
				fmt.Sprintf("--ssl-mode=%s", params.EffectiveSslMode()))
		} else {
			args = append(args,
				"--ssl",
				"--ssl-verify-server-cert")
		}
		args = append(args,
			"--ssl-ca", params.SslCa,
			"--ssl-cert", params.SslCert,
			"--ssl-key", params.SslKey)
	}
	env := []string{
		"LD_LIBRARY_PATH=" + path.Join(dir, "lib/mysql"),
	}

	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		return output, false
	}
	return output, true
}

// binaryPath does a limited path lookup for a command,
// searching only within sbin and bin in the given root.
//
// FIXME(alainjobart) move this to vt/env, and use it from
// go/vt/mysqlctl too.
func binaryPath(root, binary string) (string, error) {
	subdirs := []string{"sbin", "bin"}
	for _, subdir := range subdirs {
		binPath := path.Join(root, subdir, binary)
		if _, err := os.Stat(binPath); err == nil {
			return binPath, nil
		}
	}
	return "", fmt.Errorf("%s not found in any of %s/{%s}",
		binary, root, strings.Join(subdirs, ","))
}

func TestMain(m *testing.M) {
	flag.Parse() // Do not remove this comment, import into google3 depends on it

	exitCode := func() int {
		// Create the certs.
		root, err := os.MkdirTemp("", "TestTLSServer")
		if err != nil {
			fmt.Fprintf(os.Stderr, "TempDir failed: %v", err)
			return 1
		}
		defer os.RemoveAll(root)
		tlstest.CreateCA(root)
		tlstest.CreateSignedCert(root, tlstest.CA, "01", "server", "localhost")
		tlstest.CreateSignedCert(root, tlstest.CA, "02", "client", "Client Cert")

		// Create the extra SSL my.cnf lines.
		cnf := fmt.Sprintf(`
ssl-ca=%v/ca-cert.pem
ssl-cert=%v/server-cert.pem
ssl-key=%v/server-key.pem
`, root, root, root)
		extraMyCnf := path.Join(root, "ssl_my.cnf")
		if err := os.WriteFile(extraMyCnf, []byte(cnf), os.ModePerm); err != nil {
			fmt.Fprintf(os.Stderr, "os.WriteFile(%v) failed: %v", extraMyCnf, err)
			return 1
		}

		// For LargeQuery tests
		cnf = "max_allowed_packet=100M\n"
		maxPacketMyCnf := path.Join(root, "max_packet.cnf")
		if err := os.WriteFile(maxPacketMyCnf, []byte(cnf), os.ModePerm); err != nil {
			fmt.Fprintf(os.Stderr, "os.WriteFile(%v) failed: %v", maxPacketMyCnf, err)
			return 1
		}

		// Launch MySQL.
		// We need a Keyspace in the topology, so the DbName is set.
		// We need a Shard too, so the database 'vttest' is created.
		cfg := vttest.Config{
			Topology: &vttestpb.VTTestTopology{
				Keyspaces: []*vttestpb.Keyspace{
					{
						Name: "vttest",
						Shards: []*vttestpb.Shard{
							{
								Name:           "0",
								DbNameOverride: "vttest",
							},
						},
					},
				},
			},
			OnlyMySQL:  true,
			ExtraMyCnf: []string{extraMyCnf, maxPacketMyCnf},
		}
		cluster := vttest.LocalCluster{
			Config: cfg,
		}
		if err := cluster.Setup(); err != nil {
			fmt.Fprintf(os.Stderr, "could not launch mysql: %v\n", err)
			return 1
		}
		defer cluster.TearDown()
		connParams = cluster.MySQLConnParams()

		// Add the SSL parts, but they're not enabled until
		// the flag is set.
		connParams.SslCa = path.Join(root, "ca-cert.pem")
		connParams.SslCert = path.Join(root, "client-cert.pem")
		connParams.SslKey = path.Join(root, "client-key.pem")

		// Uncomment to sleep and be able to connect to MySQL
		// fmt.Printf("Connect to MySQL using parameters:\n")
		// json.NewEncoder(os.Stdout).Encode(connParams)
		// time.Sleep(10 * time.Minute)

		return m.Run()
	}()
	os.Exit(exitCode)
}

func failVersionDetection(err error) {
	vtenvMysqlRoot, _ := vtenv.VtMysqlRoot()
	message := fmt.Sprintf(`could not auto-detect MySQL version: %v
You may need to set your PATH so a mysqld binary can be found:
	PATH: %s
	VT_MYSQL_ROOT: %s
	VTROOT: %s
	vtenv.VtMysqlRoot(): %s
	`,
		err,
		os.Getenv("PATH"),
		os.Getenv("VT_MYSQL_ROOT"),
		os.Getenv("VTROOT"),
		vtenvMysqlRoot)
	panic(message)
}
