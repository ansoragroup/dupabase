package platform

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
)

// PostgreSQL workers receive connection credentials and a small environment,
// never the platform's JWT, operator database, admin or cloud secrets.
func pgCommandEnv(dbURL string) []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return env
	}
	password, _ := u.User.Password()
	env = append(env, "PGPASSWORD="+password)
	options := map[string]string{
		"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT",
		"sslkey": "PGSSLKEY", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR",
		"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION", "ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
		"channel_binding": "PGCHANNELBINDING", "gssencmode": "PGGSSENCMODE", "connect_timeout": "PGCONNECT_TIMEOUT",
		"application_name": "PGAPPNAME", "sslpassword": "PGSSLPASSWORD",
	}
	for key, variable := range options {
		value := u.Query().Get(key)
		if value == "" {
			value = os.Getenv(variable)
		}
		if value != "" {
			env = append(env, variable+"="+value)
		}
	}
	return env
}

type boundedCommandOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	const maxBytes = 1 << 20
	n := len(p)
	if n > maxBytes-b.Len() {
		b.truncated = true
	}
	if remaining := maxBytes - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func runBoundedCommand(cmd *exec.Cmd) ([]byte, error) {
	var output boundedCommandOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err == nil && output.truncated {
		err = fmt.Errorf("PostgreSQL command output exceeds the 1 MiB limit")
	}
	return output.Bytes(), err
}
