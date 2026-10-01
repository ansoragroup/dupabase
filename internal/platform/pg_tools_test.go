package platform

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPostgresClientMatrix(t *testing.T) {
	for server, client := range map[int]int{14: 16, 15: 16, 16: 16, 17: 17, 18: 18} {
		got, err := pgClientMajor(server)
		if err != nil || got != client {
			t.Fatalf("server %d: client %d, error %v", server, got, err)
		}
	}
	for _, server := range []int{0, 9, 13, 19, 100} {
		if _, err := pgClientMajor(server); err == nil {
			t.Fatalf("unverified major %d was accepted", server)
		}
	}
}

func TestDumpPreflightCompatibility(t *testing.T) {
	for _, tc := range []struct {
		source, producer, target int
		valid                    bool
	}{
		{14, 14, 14, true}, {14, 16, 14, true}, {15, 16, 15, true},
		{16, 16, 16, true}, {17, 17, 17, true}, {18, 18, 18, true},
		{14, 16, 18, true}, {16, 16, 17, true}, {17, 17, 18, true},
		{18, 18, 17, false}, {17, 17, 16, false}, {16, 16, 15, false},
		{14, 18, 14, false}, {16, 18, 16, false}, {18, 18, 19, false},
	} {
		content := "; Dumped from database version: " + strconv.Itoa(tc.source) + ".1\n; Dumped by pg_dump version: " + strconv.Itoa(tc.producer) + ".1\n"
		if err := validateDumpTarget(content, tc.target, true); (err == nil) != tc.valid {
			t.Fatalf("source %d producer %d target %d: %v", tc.source, tc.producer, tc.target, err)
		}
	}
	if err := validateDumpTarget("CREATE TABLE public.manual (id int);", 14, false); err != nil {
		t.Fatal("user-written SQL was rejected", err)
	}
	if err := validateDumpTarget("unrecognized archive", 18, true); err == nil {
		t.Fatal("archive without version metadata was accepted")
	}
	for _, header := range []string{
		"-- Dumped from database version: 18.6\n",
		"-- Dumped from database version 18.6\n",
		"-- Dumped by pg_dump version 18.6\n",
	} {
		if err := validateDumpTarget(header, 14, false); err == nil {
			t.Fatalf("SQL dump bypassed the downgrade check: %q", header)
		}
	}
	if err := validateDumpTarget("-- Dumped from database version 14.24\n-- Dumped by pg_dump version 16.15\n", 14, false); err != nil {
		t.Fatal("compatible plain pg_dump headers were rejected", err)
	}
}

func TestVersionedPostgresToolSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("POSTGRES_CLIENT_BIN_ROOT", root)
	for major, minor := range pgClientMinimum {
		directory := filepath.Join(root, strconv.Itoa(major))
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "pg_restore")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'pg_restore (PostgreSQL) "+strconv.Itoa(major)+"."+strconv.Itoa(minor)+"\\n'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		got, err := pgTool(context.Background(), "pg_restore", major)
		if err != nil || got != path {
			t.Fatalf("client %d: %s %v", major, got, err)
		}
	}
	if _, err := pgTool(context.Background(), "arbitrary-program", 18); err == nil {
		t.Fatal("arbitrary executable was accepted")
	}
}
