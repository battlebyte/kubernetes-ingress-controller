//go:build integration_tests

package integration

import (
	"context"
	"testing"

	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/kind"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admregv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/net"

	kongv1 "github.com/kong/kubernetes-ingress-controller/v2/pkg/apis/configuration/v1"
	"github.com/kong/kubernetes-ingress-controller/v2/pkg/clientset"
	"github.com/kong/kubernetes-ingress-controller/v2/pkg/clientset/scheme"
	"github.com/kong/kubernetes-ingress-controller/v2/test/internal/helpers"
)

func TestKongIngressValidationWebhook(t *testing.T) {
	skipTestForExpressionRouter(t)
	ctx := context.Background()

	if env.Cluster().Type() != kind.KindClusterType {
		t.Skip("webhook tests are only available on KIND clusters currently")
	}

	ns, _ := helpers.Setup(ctx, t, env)

	const configResourceName = "kong-validations-kongingress"
	ensureAdmissionRegistration(
		ctx,
		t,
		ns.Name,
		configResourceName,
		[]admregv1.RuleWithOperations{
			{
				Rule: admregv1.Rule{
					APIGroups:   []string{"configuration.konghq.com"},
					APIVersions: []string{"v1"},
					Resources:   []string{"kongingresses"},
				},
				Operations: []admregv1.OperationType{admregv1.Create, admregv1.Update},
			},
		},
	)

	t.Log("waiting for webhook service to be connective")
	ensureWebhookServiceIsConnective(ctx, t, configResourceName)

	kongClient, err := clientset.NewForConfig(env.Cluster().Config())
	require.NoError(t, err)

	t.Run("when deprecated fields are populated warnings are returned", func(t *testing.T) {
		kongIngress := &kongv1.KongIngress{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "kong-ingress-validation-",
			},
			// Proxy field is deprecated, expecting warning for it.
			Proxy: &kongv1.KongIngressService{
				Protocol: lo.ToPtr("tcp"),
			},
			// Route field is deprecated, expecting warning for it.
			Route: &kongv1.KongIngressRoute{
				Methods: []*string{lo.ToPtr("POST")},
			},
		}

		result := kongClient.ConfigurationV1().RESTClient().Post().
			Namespace(ns.Name).
			Resource("kongingresses").
			VersionedParams(&metav1.CreateOptions{}, scheme.ParameterCodec).
			Body(kongIngress).
			Do(ctx)

		assert.NoError(t, result.Error())
		require.Len(t, result.Warnings(), 2)
		expectedWarnings := []string{
			"'route' is DEPRECATED. It will have no effect. Use Ingress' annotations instead.",
			"'proxy' is DEPRECATED. It will have no effect. Use Service's annotations instead.",
		}
		receivedWarnings := lo.Map(result.Warnings(), func(item net.WarningHeader, index int) string {
			return item.Text
		})
		assert.ElementsMatch(t, expectedWarnings, receivedWarnings)
	})
}
