// Copyright (c) 2019 The Jaeger Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tlscfg

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var testCertKeyLocation = "./testdata"

func TestOptionsToConfig(t *testing.T) {
	tests := []struct {
		name        string
		options     Options
		fakeSysPool bool
		expectError string
	}{
		{
			name:    "should load system CA",
			options: Options{CAPath: ""},
		},
		{
			name:        "should fail with fake system CA",
			fakeSysPool: true,
			options:     Options{CAPath: ""},
			expectError: "fake system pool",
		},
		{
			name:    "should load custom CA",
			options: Options{CAPath: testCertKeyLocation + "/example-CA-cert.pem"},
		},
		{
			name:        "should fail with invalid CA file path",
			options:     Options{CAPath: testCertKeyLocation + "/not/valid"},
			expectError: "failed to load CA",
		},
		{
			name:        "should fail with invalid CA file content",
			options:     Options{CAPath: testCertKeyLocation + "/bad-CA-cert.txt"},
			expectError: "failed to parse CA",
		},
		{
			name: "should load valid TLS Client settings",
			options: Options{
				CAPath:   testCertKeyLocation + "/example-CA-cert.pem",
				CertPath: testCertKeyLocation + "/example-client-cert.pem",
				KeyPath:  testCertKeyLocation + "/example-client-key.pem",
			},
		},
		{
			name: "should fail with missing TLS Client Key",
			options: Options{
				CAPath:   testCertKeyLocation + "/example-CA-cert.pem",
				CertPath: testCertKeyLocation + "/example-client-cert.pem",
			},
			expectError: "both client certificate and key must be supplied",
		},
		{
			name: "should fail with invalid TLS Client Key",
			options: Options{
				CAPath:   testCertKeyLocation + "/example-CA-cert.pem",
				CertPath: testCertKeyLocation + "/example-client-cert.pem",
				KeyPath:  testCertKeyLocation + "/not/valid",
			},
			expectError: "failed to load server TLS cert and key",
		},
		{
			name: "should fail with missing TLS Client Cert",
			options: Options{
				CAPath:  testCertKeyLocation + "/example-CA-cert.pem",
				KeyPath: testCertKeyLocation + "/example-client-key.pem",
			},
			expectError: "both client certificate and key must be supplied",
		},
		{
			name: "should fail with invalid TLS Client Cert",
			options: Options{
				CAPath:   testCertKeyLocation + "/example-CA-cert.pem",
				CertPath: testCertKeyLocation + "/not/valid",
				KeyPath:  testCertKeyLocation + "/example-client-key.pem",
			},
			expectError: "failed to load server TLS cert and key",
		},
		{
			name: "should fail with invalid TLS Client CA",
			options: Options{
				ClientCAPath: testCertKeyLocation + "/not/valid",
			},
			expectError: "failed to load CA",
		},
		{
			name: "should fail with invalid TLS Client CA pool",
			options: Options{
				ClientCAPath: testCertKeyLocation + "/bad-CA-cert.txt",
			},
			expectError: "failed to parse CA",
		},
		{
			name: "should pass with valid TLS Client CA pool",
			options: Options{
				ClientCAPath: testCertKeyLocation + "/example-CA-cert.pem",
			},
		},
		{
			name: "should fail with invalid TLS Cipher Suite",
			options: Options{
				CipherSuites: []string{"TLS_INVALID_CIPHER_SUITE"},
			},
			expectError: "failed to get cipher suite ids from cipher suite names: cipher suite TLS_INVALID_CIPHER_SUITE not supported or doesn't exist",
		},
		{
			name: "should fail with invalid TLS Min Version",
			options: Options{
				MinVersion: "Invalid",
			},
			expectError: "failed to get minimum tls version",
		},
		{
			name: "should fail with invalid TLS Max Version",
			options: Options{
				MaxVersion: "Invalid",
			},
			expectError: "failed to get maximum tls version",
		},
		{
			name: "should fail with TLS Min Version greater than TLS Max Version error",
			options: Options{
				MinVersion: "1.2",
				MaxVersion: "1.1",
			},
			expectError: "minimum tls version can't be greater than maximum tls version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.fakeSysPool {
				saveSystemCertPool := systemCertPool
				systemCertPool = func() (*x509.CertPool, error) {
					return nil, fmt.Errorf("fake system pool")
				}
				defer func() {
					systemCertPool = saveSystemCertPool
				}()
			}
			cfg, err := test.options.Config(zap.NewNop())
			if test.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.expectError)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, cfg)

				if test.options.CertPath != "" && test.options.KeyPath != "" {
					c, e := tls.LoadX509KeyPair(filepath.Clean(test.options.CertPath), filepath.Clean(test.options.KeyPath))
					require.NoError(t, e)
					cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
					require.NoError(t, err)
					assert.Equal(t, &c, cert)
					cert, err = cfg.GetClientCertificate(&tls.CertificateRequestInfo{})
					require.NoError(t, err)
					assert.Equal(t, &c, cert)
				}
			}
			require.NoError(t, test.options.Close())
		})
	}
}

func TestConcurrentCertPoolAccessForDataRace(t *testing.T) {
	certPath := filepath.Join(testCertKeyLocation, "example-CA-cert.pem")
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("Failed to read certificate file: %v", err)
	}

	certPool := x509.NewCertPool()

	// This function attempts to append certs from PEM concurrently.
	appendCertsConcurrently := func(wg *sync.WaitGroup) {
		defer wg.Done()
		if !certPool.AppendCertsFromPEM(certBytes) {
			t.Error("Failed to append certs from PEM")
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go appendCertsConcurrently(&wg)
	}

	wg.Wait()
}

func TestConcurrentConfigAccess(t *testing.T) {
	logger := zap.NewNop()
	options := Options{
		Enabled:    true,
		CAPath:     testCertKeyLocation + "/example-CA-cert.pem",
		CertPath:   testCertKeyLocation + "/example-client-cert.pem",
		KeyPath:    testCertKeyLocation + "/example-client-key.pem",
		ServerName: "localhost",
	}

	// This will attempt to concurrently load the TLS config by invoking the Config method of the Options struct.
	loadTLSConfigConcurrently := func(wg *sync.WaitGroup) {
		defer wg.Done()
		if _, err := options.Config(logger); err != nil {
			t.Errorf("Failed to load TLS config: %v", err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go loadTLSConfigConcurrently(&wg)
	}

	wg.Wait()
}
