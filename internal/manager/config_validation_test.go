package manager_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/samber/mo"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/kong/kubernetes-ingress-controller/v2/internal/adminapi"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/controllers/gateway"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/manager"
)

func TestConfigValidatedVars(t *testing.T) {
	type testCase struct {
		Input                      string
		ExpectedValue              any
		ExtractValueFn             func(c manager.Config) any
		ExpectedErrorContains      string
		ExpectedUsageAdditionalMsg string
	}

	testCasesGroupedByFlag := map[string][]testCase{
		"--gateway-api-controller-name": {
			{
				Input: "example.com/controller-name",
				ExtractValueFn: func(c manager.Config) any {
					return c.GatewayAPIControllerName
				},
				ExpectedValue: "example.com/controller-name",
			},
			{
				Input: "",
				ExtractValueFn: func(c manager.Config) any {
					return c.GatewayAPIControllerName
				},
				ExpectedValue: string(gateway.GetControllerName()),
			},
			{
				Input:                 "%invalid_controller_name$",
				ExpectedErrorContains: "the expected format is example.com/controller-name",
			},
		},
		"--publish-service": {
			{
				Input: "namespace/servicename",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressService
				},
				ExpectedValue:              mo.Some(k8stypes.NamespacedName{Namespace: "namespace", Name: "servicename"}),
				ExpectedUsageAdditionalMsg: "Flag --publish-service has been deprecated, Use --ingress-service instead\n",
			},
			{
				Input:                 "servicename",
				ExpectedErrorContains: "the expected format is namespace/name",
			},
		},
		"--ingress-service": {
			{
				Input: "namespace/servicename",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressService
				},
				ExpectedValue: mo.Some(k8stypes.NamespacedName{Namespace: "namespace", Name: "servicename"}),
			},
			{
				Input:                 "servicename",
				ExpectedErrorContains: "the expected format is namespace/name",
			},
		},
		"--publish-status-address": {
			{
				Input: "192.0.2.42,some-dns-name,192.0.2.43",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressAddresses
				},
				ExpectedValue:              []string{"192.0.2.42", "some-dns-name", "192.0.2.43"},
				ExpectedUsageAdditionalMsg: "Flag --publish-status-address has been deprecated, Use --ingress-address instead\n",
			},
		},
		"--ingress-address": {
			{
				Input: "192.0.2.42,some-dns-name,192.0.2.43",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressAddresses
				},
				ExpectedValue: []string{"192.0.2.42", "some-dns-name", "192.0.2.43"},
			},
		},
		"--publish-service-udp": {
			{
				Input: "namespace/servicename",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressServiceUDP
				},
				ExpectedValue:              mo.Some(k8stypes.NamespacedName{Namespace: "namespace", Name: "servicename"}),
				ExpectedUsageAdditionalMsg: "Flag --publish-service-udp has been deprecated, Use --ingress-service-udp instead\n",
			},
			{
				Input:                 "servicename",
				ExpectedErrorContains: "the expected format is namespace/name",
			},
		},
		"--ingress-service-udp": {
			{
				Input: "namespace/servicename",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressServiceUDP
				},
				ExpectedValue: mo.Some(k8stypes.NamespacedName{Namespace: "namespace", Name: "servicename"}),
			},
			{
				Input:                 "servicename",
				ExpectedErrorContains: "the expected format is namespace/name",
			},
		},
		"--publish-status-address-udp": {
			{
				Input: "192.0.2.42,some-dns-name,192.0.2.43",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressAddressesUDP
				},
				ExpectedValue:              []string{"192.0.2.42", "some-dns-name", "192.0.2.43"},
				ExpectedUsageAdditionalMsg: "Flag --publish-status-address-udp has been deprecated, Use --ingress-address-udp instead\n",
			},
		},
		"--ingress-address-udp": {
			{
				Input: "192.0.2.42,some-dns-name,192.0.2.43",
				ExtractValueFn: func(c manager.Config) any {
					return c.IngressAddressesUDP
				},
				ExpectedValue: []string{"192.0.2.42", "some-dns-name", "192.0.2.43"},
			},
		},
		"--kong-admin-svc": {
			{
				Input: "namespace/servicename",
				ExtractValueFn: func(c manager.Config) any {
					return c.KongAdminSvc
				},
				ExpectedValue: mo.Some(k8stypes.NamespacedName{Namespace: "namespace", Name: "servicename"}),
			},
			{
				Input:                 "namespace/",
				ExpectedErrorContains: "name cannot be empty",
			},
			{
				Input:                 "/name",
				ExpectedErrorContains: "namespace cannot be empty",
			},
		},
		"--konnect-runtime-group-id": {
			{
				Input: "5ef731c0-6081-49d6-b3ec-d4f85e58b956",
				ExtractValueFn: func(c manager.Config) any {
					return c.Konnect.ControlPlaneID
				},
				ExpectedValue:              "5ef731c0-6081-49d6-b3ec-d4f85e58b956",
				ExpectedUsageAdditionalMsg: "Flag --konnect-runtime-group-id has been deprecated, Use --konnect-control-plane-id instead.\n",
			},
		},
		"--konnect-control-plane-id": {
			{
				Input: "5ef731c0-6081-49d6-b3ec-d4f85e58b956",
				ExtractValueFn: func(c manager.Config) any {
					return c.Konnect.ControlPlaneID
				},
				ExpectedValue: "5ef731c0-6081-49d6-b3ec-d4f85e58b956",
			},
		},
	}

	for flag, flagTestCases := range testCasesGroupedByFlag {
		for _, tc := range flagTestCases {
			t.Run(fmt.Sprintf("%s=%s", flag, tc.Input), func(t *testing.T) {
				var c manager.Config
				var input []string
				if tc.Input != "" {
					input = []string{flag, tc.Input}
				}

				flagSet := c.FlagSet()
				var usageAdditionalMsg bytes.Buffer
				flagSet.SetOutput(&usageAdditionalMsg)

				err := flagSet.Parse(input)
				if tc.ExpectedErrorContains != "" {
					require.ErrorContains(t, err, tc.ExpectedErrorContains)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.ExpectedValue, tc.ExtractValueFn(c))
				require.Equal(t, tc.ExpectedUsageAdditionalMsg, usageAdditionalMsg.String())
			})
		}
	}
}

func TestConfigValidate(t *testing.T) {
	t.Run("konnect", func(t *testing.T) {
		validEnabled := func() *manager.Config {
			return &manager.Config{
				KongAdminSvc: mo.Some(k8stypes.NamespacedName{Name: "admin-svc", Namespace: "ns"}),
				Konnect: adminapi.KonnectConfig{
					ConfigSynchronizationEnabled: true,
					ControlPlaneID:               "fbd3036f-0f1c-4e98-b71c-d4cd61213f90",
					Address:                      "https://us.kic.api.konghq.tech",
					TLSClient: adminapi.TLSClientConfig{
						// We do not set valid cert or key, and it's still considered valid as at this level we only care
						// about them being not empty. Their validity is to be verified later on by the Admin API client
						// constructor.
						Cert: "not-empty-cert",
						Key:  "not-empty-key",
					},
				},
			}
		}

		t.Run("disabled should not require other vars to be set", func(t *testing.T) {
			c := &manager.Config{Konnect: adminapi.KonnectConfig{ConfigSynchronizationEnabled: false}}
			require.NoError(t, c.Validate())
		})

		t.Run("enabled should require tls client config", func(t *testing.T) {
			require.NoError(t, validEnabled().Validate())
		})

		t.Run("enabled with no tls config is rejected", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.TLSClient = adminapi.TLSClientConfig{}
			require.ErrorContains(t, c.Validate(), "missing TLS client configuration")
		})

		t.Run("enabled with no tls key is rejected", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.TLSClient.Key = ""
			require.ErrorContains(t, c.Validate(), "client certificate was provided, but the client key was not")
		})

		t.Run("enabled with no tls cert is rejected", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.TLSClient.Cert = ""
			require.ErrorContains(t, c.Validate(), "client key was provided, but the client certificate was not")
		})

		t.Run("enabled with tls cert file instead of cert is accepted", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.TLSClient.Cert = ""
			c.Konnect.TLSClient.CertFile = "non-empty-path"
			require.NoError(t, c.Validate())
		})

		t.Run("enabled with tls key file instead of key is accepted", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.TLSClient.Key = ""
			c.Konnect.TLSClient.KeyFile = "non-empty-path"
			require.NoError(t, c.Validate())
		})

		t.Run("enabled with no control plane is rejected", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.ControlPlaneID = ""
			require.ErrorContains(t, c.Validate(), "control plane not specified")
		})

		t.Run("enabled with no address is rejected", func(t *testing.T) {
			c := validEnabled()
			c.Konnect.Address = ""
			require.ErrorContains(t, c.Validate(), "address not specified")
		})

		t.Run("enabled with no gateway service discovery enabled", func(t *testing.T) {
			c := validEnabled()
			c.KongAdminSvc = manager.OptionalNamespacedName{}
			require.ErrorContains(t, c.Validate(), "--kong-admin-svc has to be set when using --konnect-sync-enabled")
		})
	})

	t.Run("Admin API", func(t *testing.T) {
		validWithClientTLS := func() manager.Config {
			return manager.Config{
				KongAdminAPIConfig: adminapi.HTTPClientOpts{
					TLSClient: adminapi.TLSClientConfig{
						// We do not set valid cert or key, and it's still considered valid as at this level we only care
						// about them being not empty. Their validity is to be verified later on by the Admin API client
						// constructor.
						Cert: "not-empty-cert",
						Key:  "not-empty-key",
					},
				},
			}
		}

		t.Run("no TLS client is allowed", func(t *testing.T) {
			c := manager.Config{
				KongAdminAPIConfig: adminapi.HTTPClientOpts{
					TLSClient: adminapi.TLSClientConfig{},
				},
			}
			require.NoError(t, c.Validate())
		})

		t.Run("valid TLS client is allowed", func(t *testing.T) {
			c := validWithClientTLS()
			require.NoError(t, c.Validate())
		})

		t.Run("missing tls key is rejected", func(t *testing.T) {
			c := validWithClientTLS()
			c.KongAdminAPIConfig.TLSClient.Key = ""
			require.ErrorContains(t, c.Validate(), "client certificate was provided, but the client key was not")
		})

		t.Run("missing tls cert is rejected", func(t *testing.T) {
			c := validWithClientTLS()
			c.KongAdminAPIConfig.TLSClient.Cert = ""
			require.ErrorContains(t, c.Validate(), "client key was provided, but the client certificate was not")
		})

		t.Run("tls cert file instead of cert is accepted", func(t *testing.T) {
			c := validWithClientTLS()
			c.KongAdminAPIConfig.TLSClient.Cert = ""
			c.KongAdminAPIConfig.TLSClient.CertFile = "non-empty-path"
			require.NoError(t, c.Validate())
		})

		t.Run("tls key file instead of key is accepted", func(t *testing.T) {
			c := validWithClientTLS()
			c.KongAdminAPIConfig.TLSClient.Key = ""
			c.KongAdminAPIConfig.TLSClient.KeyFile = "non-empty-path"
			require.NoError(t, c.Validate())
		})
	})

	t.Run("Admin Token", func(t *testing.T) {
		validWithToken := func() manager.Config {
			return manager.Config{
				KongAdminToken: "non-empty-token",
			}
		}

		t.Run("admin token accepted", func(t *testing.T) {
			c := validWithToken()
			require.NoError(t, c.Validate())
		})
	})

	t.Run("Admin Token Path", func(t *testing.T) {
		validWithTokenPath := func() manager.Config {
			return manager.Config{
				KongAdminTokenPath: "non-empty-token-path",
			}
		}

		t.Run("admin token and token path rejected", func(t *testing.T) {
			c := validWithTokenPath()
			c.KongAdminToken = "non-empty-token"
			require.ErrorContains(t, c.Validate(), "both admin token and admin token file specified, only one allowed")
		})
	})
}

func TestConfigValidateGatewayDiscovery(t *testing.T) {
	testCases := []struct {
		name             string
		gatewayDiscovery bool
		dbMode           string
		expectError      bool
	}{
		{
			name:             "gateway discovery disabled should pass in db-less mode",
			gatewayDiscovery: false,
			dbMode:           "off",
			expectError:      false,
		},
		{
			name:             "gateway discovery disabled should pass in db-backed mode",
			gatewayDiscovery: false,
			dbMode:           "postgres",
			expectError:      false,
		},
		{
			name:             "gateway discovery enabled should pass in db-less mode",
			gatewayDiscovery: true,
			dbMode:           "",
			expectError:      false,
		},
		{
			name:             "gateway discovery enabled should not pass in db-backed mode",
			gatewayDiscovery: true,
			dbMode:           "postgres",
			expectError:      true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := &manager.Config{}
			if tc.gatewayDiscovery {
				c.KongAdminSvc = mo.Some(k8stypes.NamespacedName{Name: "admin-svc", Namespace: "ns"})
			}
			err := c.ValidateGatewayDiscovery(tc.dbMode)
			if !tc.expectError {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
