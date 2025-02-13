package translators

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/kong/go-kong/kong"
	"github.com/samber/lo"
	netv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kong/kubernetes-ingress-controller/v2/internal/annotations"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/dataplane/kongstate"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/util"
	kongv1alpha1 "github.com/kong/kubernetes-ingress-controller/v2/pkg/apis/configuration/v1alpha1"
)

// -----------------------------------------------------------------------------
// Ingress Translation - Public Functions
// -----------------------------------------------------------------------------

type TranslatedKubernetesObjectsCollector interface {
	Add(client.Object)
}

type TranslateIngressFeatureFlags struct {
	// ExpressionRoutes indicates whether to translate Kubernetes objects to expression based Kong Routes.
	ExpressionRoutes bool
}

// TranslateIngresses receives a slice of Kubernetes Ingress objects and produces a translated set of kong.Services
// and kong.Routes which will come wrapped in a kongstate.Service object.
func TranslateIngresses(
	ingresses []*netv1.Ingress,
	icp kongv1alpha1.IngressClassParametersSpec,
	flags TranslateIngressFeatureFlags,
	translatedObjectsCollector TranslatedKubernetesObjectsCollector,
) map[string]kongstate.Service {
	index := newIngressTranslationIndex(flags)
	for _, ingress := range ingresses {
		prependRegexPrefix := MaybePrependRegexPrefixForIngressV1Fn(ingress, icp.EnableLegacyRegexDetection)
		index.Add(ingress, prependRegexPrefix)
		translatedObjectsCollector.Add(ingress)
	}

	return index.Translate()
}

// -----------------------------------------------------------------------------
// Ingress Translation - Private Consts & Vars
// -----------------------------------------------------------------------------

var defaultHTTPIngressPathType = netv1.PathTypeImplementationSpecific

const (
	defaultHTTPPort = 80
	defaultRetries  = 5

	// defaultServiceTimeout indicates the amount of time by default that we wait
	// for connections to an underlying Kubernetes service to complete in the
	// data-plane. The current value is based on a historical default that started
	// in version 0 of the ingress controller.
	defaultServiceTimeout = time.Second * 60
)

// -----------------------------------------------------------------------------
// Ingress Translation - Private - Index
// -----------------------------------------------------------------------------

// ingressTranslationIndex is a de-duplicating index of the contents of a
// Kubernetes Ingress resource, where the key is a combination of data from
// that resource which makes it unique for the purpose of translating it into
// kong.Services and kong.Routes and the value is a combination of various
// metadata needed to configure and name those kong.Services and kong.Routes
// plus the URL paths which make those routes actionable. This index is used
// to enable compiling a minimal set of kong.Routes when translating into
// Kong resources for each rule in the ingress spec, where the combination of:
//
// - ingress.Namespace
// - ingress.Name
// - host for the Ingress rule
// - Kubernetes Service for the Ingress rule
// - the port for the Kubernetes Service
//
// are unique. For ingress spec rules which are not unique along those
// data-points, a separate kong.Service and separate kong.Routes will be created
// for each unique combination.
type ingressTranslationIndex struct {
	cache        map[string]*ingressTranslationMeta
	featureFlags TranslateIngressFeatureFlags
}

func newIngressTranslationIndex(flags TranslateIngressFeatureFlags) *ingressTranslationIndex {
	return &ingressTranslationIndex{
		cache:        make(map[string]*ingressTranslationMeta),
		featureFlags: flags,
	}
}

type addRegexPrefixFn func(string) *string

func (i *ingressTranslationIndex) Add(ingress *netv1.Ingress, addRegexPrefix addRegexPrefixFn) {
	for _, ingressRule := range ingress.Spec.Rules {
		if ingressRule.HTTP == nil || len(ingressRule.HTTP.Paths) < 1 {
			continue
		}

		for _, httpIngressPath := range ingressRule.HTTP.Paths {
			httpIngressPath := httpIngressPath
			httpIngressPath.Path = flattenMultipleSlashes(httpIngressPath.Path)

			if httpIngressPath.Path == "" {
				httpIngressPath.Path = "/"
			}

			if httpIngressPath.PathType == nil {
				httpIngressPath.PathType = &defaultHTTPIngressPathType
			}

			serviceName := httpIngressPath.Backend.Service.Name
			port := PortDefFromServiceBackendPort(&httpIngressPath.Backend.Service.Port)

			cacheKey := fmt.Sprintf("%s.%s.%s.%s.%s", ingress.Namespace, ingress.Name, ingressRule.Host, serviceName, port.CanonicalString())
			meta, ok := i.cache[cacheKey]
			if !ok {
				meta = &ingressTranslationMeta{
					ingressNamespace: ingress.Namespace,
					ingressName:      ingress.Name,
					ingressUID:       string(ingress.UID),
					ingressHost:      ingressRule.Host,
					ingressTags:      util.GenerateTagsForObject(ingress),
					serviceName:      serviceName,
					servicePort:      port,
					addRegexPrefixFn: addRegexPrefix,
				}
			}

			meta.parentIngress = ingress
			meta.paths = append(meta.paths, httpIngressPath)
			i.cache[cacheKey] = meta
		}
	}
}

func (i *ingressTranslationIndex) Translate() map[string]kongstate.Service {
	kongStateServiceCache := make(map[string]kongstate.Service)
	for _, meta := range i.cache {
		kongServiceName := meta.generateKongServiceName()
		kongStateService, ok := kongStateServiceCache[kongServiceName]
		if !ok {
			kongStateService = meta.translateIntoKongStateService(kongServiceName, meta.servicePort)
		}

		if i.featureFlags.ExpressionRoutes {
			route := meta.translateIntoKongExpressionRoute()
			kongStateService.Routes = append(kongStateService.Routes, *route)
		} else {
			route := meta.translateIntoKongRoute()
			kongStateService.Routes = append(kongStateService.Routes, *route)
		}

		kongStateServiceCache[kongServiceName] = kongStateService
	}

	return kongStateServiceCache
}

// -----------------------------------------------------------------------------
// Ingress Translation - Private - Metadata
// -----------------------------------------------------------------------------

type ingressTranslationMeta struct {
	parentIngress    client.Object
	ingressNamespace string
	ingressName      string
	ingressUID       string
	ingressHost      string
	ingressTags      []*string
	serviceName      string
	servicePort      kongstate.PortDef
	paths            []netv1.HTTPIngressPath
	addRegexPrefixFn addRegexPrefixFn
}

func (m *ingressTranslationMeta) translateIntoKongStateService(kongServiceName string, portDef kongstate.PortDef) kongstate.Service {
	return kongstate.Service{
		Namespace: m.parentIngress.GetNamespace(),
		Service: kong.Service{
			Name:           kong.String(kongServiceName),
			Host:           kong.String(fmt.Sprintf("%s.%s.%s.svc", m.serviceName, m.parentIngress.GetNamespace(), portDef.CanonicalString())),
			Port:           kong.Int(defaultHTTPPort),
			Protocol:       kong.String("http"),
			Path:           kong.String("/"),
			ConnectTimeout: kong.Int(int(defaultServiceTimeout.Milliseconds())),
			ReadTimeout:    kong.Int(int(defaultServiceTimeout.Milliseconds())),
			WriteTimeout:   kong.Int(int(defaultServiceTimeout.Milliseconds())),
			Retries:        kong.Int(defaultRetries),
		},
		Backends: []kongstate.ServiceBackend{{
			Name:      m.serviceName,
			Namespace: m.parentIngress.GetNamespace(),
			PortDef:   portDef,
		}},
		Parent: m.parentIngress,
	}
}

func (m *ingressTranslationMeta) generateKongServiceName() string {
	return fmt.Sprintf(
		"%s.%s.%s",
		m.parentIngress.GetNamespace(),
		m.serviceName,
		m.servicePort.CanonicalString(),
	)
}

func (m *ingressTranslationMeta) translateIntoKongRoute() *kongstate.Route {
	ingressHost := m.ingressHost
	if strings.Contains(ingressHost, "*") {
		// '_' is not allowed in host, so we use '_' to replace '*' since '*' is not allowed in Kong.
		ingressHost = strings.ReplaceAll(ingressHost, "*", "_")
	}
	routeName := fmt.Sprintf(
		"%s.%s.%s.%s.%s",
		m.parentIngress.GetNamespace(),
		m.parentIngress.GetName(),
		m.serviceName,
		ingressHost,
		m.servicePort.CanonicalString(),
	)
	route := &kongstate.Route{
		Ingress: util.K8sObjectInfo{
			Namespace:   m.parentIngress.GetNamespace(),
			Name:        m.parentIngress.GetName(),
			Annotations: m.parentIngress.GetAnnotations(),
		},
		Route: kong.Route{
			Name:              kong.String(routeName),
			StripPath:         kong.Bool(false),
			PreserveHost:      kong.Bool(true),
			Protocols:         kong.StringSlice("http", "https"),
			RegexPriority:     kong.Int(0),
			RequestBuffering:  kong.Bool(true),
			ResponseBuffering: kong.Bool(true),
			Tags:              m.ingressTags,
		},
	}

	if m.ingressHost != "" {
		route.Route.Hosts = append(route.Route.Hosts, kong.String(m.ingressHost))
	}

	for _, httpIngressPath := range m.paths {
		paths := PathsFromIngressPaths(httpIngressPath)
		for i, path := range paths {
			paths[i] = m.addRegexPrefixFn(*path)
		}
		route.Paths = append(route.Paths, paths...)
	}

	return route
}

// -----------------------------------------------------------------------------
// Ingress Translation - Private - Helper Functions
// -----------------------------------------------------------------------------

// TODO this is exported because most of the parser translate functions are still in the parser package. if/when we
// refactor to move them here, this should become private.

// PathsFromIngressPaths takes a path and Ingress path type and returns a set of Kong route paths that satisfy that path
// type. It optionally adds the Kong 3.x regex path prefix for path types that require a regex path. It rejects
// unknown path types with an error.
func PathsFromIngressPaths(httpIngressPath netv1.HTTPIngressPath) []*string {
	routePaths := []string{}
	routeRegexPaths := []string{}
	if httpIngressPath.PathType == nil {
		return nil
	}

	switch *httpIngressPath.PathType {
	case netv1.PathTypePrefix:
		base := strings.Trim(httpIngressPath.Path, "/")
		if base == "" {
			routePaths = append(routePaths, "/")
		} else {
			routePaths = append(routePaths, "/"+base+"/")
			routeRegexPaths = append(routeRegexPaths, KongPathRegexPrefix+"/"+base+"$")
		}
	case netv1.PathTypeExact:
		relative := strings.TrimLeft(httpIngressPath.Path, "/")
		routeRegexPaths = append(routeRegexPaths, KongPathRegexPrefix+"/"+relative+"$")
	case netv1.PathTypeImplementationSpecific:
		if httpIngressPath.Path == "" {
			routePaths = append(routePaths, "/")
		} else {
			routePaths = append(routePaths, httpIngressPath.Path)
		}
	default:
		// the default case here is mostly to provide a home for this comment: we explicitly do not handle unknown
		// PathTypes, and leave it up to the callers if they want to handle empty responses. barring spec changes,
		// however, this should not be a concern: Kubernetes rejects any Ingress with an unknown PathType already, so
		// none should ever end up here. prior versions of this function returned an error in this case, but it
		// should be unnecessary in practice and not returning one simplifies the call chain above (this would be the
		// only part of translation that can error)
		return nil
	}

	routePaths = append(routePaths, routeRegexPaths...)
	return kong.StringSlice(routePaths...)
}

func flattenMultipleSlashes(path string) string {
	var out []rune
	in := []rune(path)
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == '/' {
			for i < len(in)-1 && in[i+1] == '/' {
				i++
			}
		}
		out = append(out, c)
	}
	return string(out)
}

// legacyRegexPathExpression is the regular expression used by Kong <3.0 to determine if a path is not a regex.
var legacyRegexPathExpression = regexp.MustCompile(`^[a-zA-Z0-9\.\-_~/%]*$`)

// MaybePrependRegexPrefix takes a path, controller regex prefix, and a legacy heuristic toggle. It returns the path
// with the Kong regex path prefix if it either began with the controller prefix or did not, but matched the legacy
// heuristic, and the heuristic was enabled.
func MaybePrependRegexPrefix(path, controllerPrefix string, applyLegacyHeuristic bool) string {
	if strings.HasPrefix(path, controllerPrefix) {
		path = strings.Replace(path, controllerPrefix, KongPathRegexPrefix, 1)
	} else if applyLegacyHeuristic {
		// this regex matches if the path _is not_ considered a regex by Kong 2.x
		if legacyRegexPathExpression.FindString(path) == "" {
			if !strings.HasPrefix(path, KongPathRegexPrefix) {
				path = KongPathRegexPrefix + path
			}
		}
	}
	return path
}

// MaybePrependRegexPrefixForIngressV1Fn returns a function that prepends a regex prefix to a path for a given netv1.Ingress.
func MaybePrependRegexPrefixForIngressV1Fn(ingress *netv1.Ingress, applyLegacyHeuristic bool) func(path string) *string {
	// If the ingress has a regex prefix annotation, use that, otherwise use the controller default.
	regexPrefix := ControllerPathRegexPrefix
	if prefix, ok := ingress.ObjectMeta.Annotations[annotations.AnnotationPrefix+annotations.RegexPrefixKey]; ok {
		regexPrefix = prefix
	}

	return func(path string) *string {
		return lo.ToPtr(MaybePrependRegexPrefix(path, regexPrefix, applyLegacyHeuristic))
	}
}

type runeType int

const (
	runeTypeEscape runeType = iota
	runeTypeMark
	runeTypeDigit
	runeTypePlain
)

// generateRewriteURIConfig parses uri with SM of four states.
// `runeTypeEscape` indicates `\` encountered and `$` expected, the SM state will transfer
// to `runeTypePlain`.
// `runeTypeMark` indicates `$` encountered and digit expected, the SM state will transfer
// to `runeTypeDigit`.
// `runeTypeDigit` indicates digit encountered and digit expected. If the following
// character is still digit, the SM state will remain unchanged. Otherwise, a new capture
// group will be created and the SM state will transfer to `runeTypePlain`.
// `runeTypePlain` indicates the following character is plain text other than `$` and `\`.
// The former will cause the SM state to transfer to `runeTypeMark` and the latter will
// cause the SM state to transfer to `runeTypeEscape`.
func generateRewriteURIConfig(uri string) (string, error) {
	out := strings.Builder{}
	lastRuneType := runeTypePlain
	for i, char := range uri {
		switch lastRuneType {
		case runeTypeEscape:
			if char != '$' {
				return "", fmt.Errorf("unexpected %c at pos %d", char, i)
			}

			out.WriteRune(char)
			lastRuneType = runeTypePlain

		case runeTypeMark:
			if !unicode.IsDigit(char) {
				return "", fmt.Errorf("unexpected %c at pos %d", char, i)
			}

			out.WriteString("$(uri_captures[")
			out.WriteRune(char)
			lastRuneType = runeTypeDigit

		case runeTypeDigit:
			if unicode.IsDigit(char) {
				out.WriteRune(char)
			} else {
				out.WriteString("])")
				if char == '$' {
					lastRuneType = runeTypeMark
				} else if char == '\\' {
					lastRuneType = runeTypeEscape
				} else {
					out.WriteRune(char)
					lastRuneType = runeTypePlain
				}
			}

		case runeTypePlain:
			if char == '$' {
				lastRuneType = runeTypeMark
			} else if char == '\\' {
				lastRuneType = runeTypeEscape
			} else {
				out.WriteRune(char)
			}
		}
	}

	if lastRuneType == runeTypeDigit {
		out.WriteString("])")
		lastRuneType = runeTypePlain
	}

	if lastRuneType != runeTypePlain {
		return "", fmt.Errorf("unexpected end of string")
	}

	return out.String(), nil
}

// MaybeRewriteURI appends a request-transformer plugin if the value of konghq.com/rewrite annotation is valid.
func MaybeRewriteURI(service *kongstate.Service, rewriteURIEnable bool) error {
	rewriteURI, exists := annotations.ExtractRewriteURI(service.Parent.GetAnnotations())
	if !exists {
		return nil
	}

	if !rewriteURIEnable {
		return fmt.Errorf("konghq.com/rewrite annotation not supported when rewrite uris disabled")
	}

	if rewriteURI == "" {
		rewriteURI = "/"
	}

	config, err := generateRewriteURIConfig(rewriteURI)
	if err != nil {
		return err
	}

	service.Plugins = append(service.Plugins, kong.Plugin{
		Name: kong.String("request-transformer"),
		Config: kong.Configuration{
			"replace": map[string]string{
				"uri": config,
			},
		},
	})

	return nil
}
