#!/bin/bash
#
# Validate that the current version of the generated parser matches the output
# generated by the version of goyacc installed on the local system.
#
# This is used in Travis to verify that the currently committed version was
# generated with the proper version of goyacc.

source build.env

CUR="sql.go"
TMP="/tmp/sql.$$.go"

set -e

if ! cd go/vt/sqlparser/ ; then
        echo "ERROR: $0 must be run in the root project directory"
        exit 1
fi

mv $CUR $TMP
output=$(go run ./goyacc -fo $CUR sql.y)
expectedOutput=$'\nconflicts: 5 shift/reduce'

if [[ "$output" != "$expectedOutput" ]]; then
    echo -e "Expected output from goyacc:$expectedOutput\ngot:$output"
    mv $TMP $CUR
    exit 1
fi

gofmt -w $CUR

if ! diff -q $CUR $TMP > /dev/null ; then
        echo "ERROR: Regenerated parser $TMP does not match current version $(pwd)/sql.go:"
        diff -u $CUR $TMP
        mv $TMP $CUR

        echo
        echo "Please ensure go and goyacc are up to date and re-run 'make parser' to generate."
        exit 1
fi

mv $TMP $CUR
