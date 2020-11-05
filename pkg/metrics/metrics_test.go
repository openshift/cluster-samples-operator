package metrics

import (
	"bytes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	operatorv1 "github.com/openshift/api/operator/v1"
	configv1 "github.com/openshift/api/samples/v1"
)

type fakeSecretLister struct {
	s *corev1.Secret
}

func (f *fakeSecretLister) List(labels.Selector) ([]*corev1.Secret, error) {
	if f.s == nil {
		return []*corev1.Secret{}, nil
	}
	return []*corev1.Secret{f.s}, nil
}
func (f *fakeSecretLister) Get(name string) (*corev1.Secret, error) {
	if f.s == nil {
		return nil, errors.NewNotFound(corev1.Resource("secret"), name)
	}
	return f.s, nil
}

type fakeConfigLister struct {
	c *configv1.Config
}

func (f *fakeConfigLister) List(labels.Selector) ([]*configv1.Config, error) {
	if f.c == nil {
		return []*configv1.Config{}, nil
	}
	return []*configv1.Config{f.c}, nil
}
func (f *fakeConfigLister) Get(name string) (*configv1.Config, error) {
	if f.c == nil {
		return nil, errors.NewNotFound(configv1.Resource("config"), name)
	}
	return f.c, nil
}

type fakeConfigMapLister struct {
	cms []*corev1.ConfigMap
}

func (f *fakeConfigMapLister) List(selector labels.Selector) ([]*corev1.ConfigMap, error) {
	if f.cms == nil {
		return []*corev1.ConfigMap{}, nil
	}
	return f.cms, nil
}

func (f *fakeConfigMapLister) Get(name string) (*corev1.ConfigMap, error) {
	if f.cms == nil {
		return nil, nil
	}
	for _, cm := range f.cms {
		if cm.Name == name {
			return cm, nil
		}
	}
	return nil, nil
}

type fakeResponseWriter struct {
	bytes.Buffer
	statusCode int
	header     http.Header
}

func (f *fakeResponseWriter) Header() http.Header {
	return f.header
}

func (f *fakeResponseWriter) WriteHeader(statusCode int) {
	f.statusCode = statusCode
}

func TestMetrics(t *testing.T) {
	streams = []string{"foo", "bar"}
	validTBRCred := "{\"auths\":{\"registry.svc.ci.openshift.org\":{\"auth\":\"dXNlcjozdlpKWmVjNWZiWmZOS3hTNUVsek96Q3M2VlWTBSb0ZWTWs3MWRTejRr\"},\"cloud.openshift.com\":{\"auth\":\"b3BlbnNoaWZ0LXJlbGVhc2UtV2K2dtb250ZXJvcmVkaGF0Y29tMWhycWFjY2NudXE5NHlwd2cyMG0weXBhbTNqOkJOSzlWVU01N01CVlMyWFg1NEZRQU1ZQjZSUzdHSk5WUVhIOExEQlhOUU9NSU9DMTJKRFNJVFY1NTRNVVJDRlI=\",\"email\":\"foo@redhat.com\"},\"quay.io\":{\"auth\":\"b3BlbnNoaWLXJlbGVhc2UtZGV2K2dtb250ZXJvcmVkaGF0Y29tMWhycWFjY2NudXE5NHlwd2cyMG0weXBhbTNqOkJOSzlWVU01N01CVlMyWFg1NEZRQU1ZQjZSUzdHSk5WUVhIOExEQlhOUU9NSU9DMTJKRFNJVFY1NTRNVVJDRlI=\",\"email\":\"foo@redhat.com\"},\"registry.connect.redhat.com\":{\"auth\":\"NzY3OTcyNHx1aGMUhScWFjQ2NOVVE5NFlQd2cyMG0weXBhTTNqOmV5SmhiR2NpT2lKU1V6VXhNaUo5LmV5SnpkV0lpT2lJMVlUTm1OMkkyTW1ZME5HSTBaVGM1WVROaFlqYzRZVEZrTURFMFlqRTRNeUo5LkY4d3ZXM052dmxPcHZDRVM1RTF5R25BRjNHcEwwOGhOZmF2YnlmbzBIVXAwZ0x2VXcxNVhFR3VVZzd6RVdtVmdZeExmZDlnUTFJc243R1ZocG4wN1RBNlByQ3p3OXMyYjVTUFBoZkEtamp2ZjRWdHktTHNnaHIzODRQTlJJa0lhWElPVGlsVkJId1lpVmw1TXRUSW1nbnVCOXN3RXc5RDJVMnRKZEk5RDNDTm9zVzNTZGl0WFI5S01UQXdQNm1MZDNGbWhzTUV0eGRjVGMyQTk2bGM5UWp2cFJqUHhJUXNDSDBLRzV0OF9UaU5OQUFoV19YWTYyaFZPSS1ya0lhZV9BMjhSa2hFdzNzRFVYTElMTHFoRmZqUFB3dVV0SjVUS3FSWE80Z0I0TUhMU2VOdm9Fa0Y0dloyZmRKZVRwR0R0SmZMOUgxbGI2LWNraVVDbjh3MFB6TmRmTURIaC1pNUpNNXFIOW9CbHBBSjFpcnItQlhBdWExUEtaNnVhQVZnQ25DR2I2d3JQbHVnRlp0T1B2V0dpeTRTdm5LNk9kenBPVF9LMElmNS1MSXdkM21RLXVhQlN3Uy1OVFVvcjNTcFZud0ZzNDlJblRuTC1uTWVjMkppcDdwdnhFdWdwTWk0anQ1X2plc0VHZmdyZk8waDdtQ21ZUG1TMV9LdFAtbEpkdXhqTzZXNXhHcjFJZl9FLWp3TUhCTnlKTlU5b1VsMWkwOXBmT0NZZ3dSNnJ3YlFKOFNuS3pqUEV2bTRJVTNIX2NTcFlyWVRlZUlNanEtYkppLXRJVWFFOWN0a2VZZ09rR0g2eXVGQ0pvVU9oSVJodzNPbUN4TmJIN1FnZHZMbnR2cXFDZjBQTXdnQ1BUTkdmSGRWcC01aG1YcVdsTFdkZlV3Y1ZRdy1fLWVN\",\"email\":\"foo@redhat.com\"},\"registry.redhat.io\":{\"auth\":\"NzY3OTcyNHx1aGMtMUhScWFjQ2NOVVE5NQd2cyMG0weXBhTTNqOmV5SmhiR2NpT2lKU1V6VXhNaUo5LmV5SnpkV0lpT2lJMVlUTm1OMkkyTW1ZME5HSTBaVGM1WVROaFlqYzRZVEZrTURFMFlqRTRNeUo5LkY4d3ZXM052dmxPcHZDRVM1RTF5R25BRjNHcEwwOGhOZmF2YnlmbzBIVXAwZ0x2VXcxNVhFR3VVZzd6RVdtVmdZeExmZDlnUTFJc243R1ZocG4wN1RBNlByQ3p3OXMyYjVTUFBoZkEtamp2ZjRWdHktTHNnaHIzODRQTlJJa0lhWElPVGlsVkJId1lpVmw1TXRUSW1nbnVCOXN3RXc5RDJVMnRKZEk5RDNDTm9zVzNTZGl0WFI5S01UQXdQNm1MZDNGbWhzTUV0eGRjVGMyQTk2bGM5UWp2cFJqUHhJUXNDSDBLRzV0OF9UaU5OQUFoV19YWTYyaFZPSS1ya0lhZV9BMjhSa2hFdzNzRFVYTElMTHFoRmZqUFB3dVV0SjVUS3FSWE80Z0I0TUhMU2VOdm9Fa0Y0dloyZmRKZVRwR0R0SmZMOUgxbGI2LWNraVVDbjh3MFB6TmRmTURIaC1pNUpNNXFIOW9CbHBBSjFpcnItQlhBdWExUEtaNnVhQVZnQ25DR2I2d3JQbHVnRlp0T1B2V0dpeTRTdm5LNk9kenBPVF9LMElmNS1MSXdkM21RLXVhQlN3Uy1OVFVvcjNTcFZud0ZzNDlJblRuTC1uTWVjMkppcDdwdnhFdWdwTWk0anQ1X2plc0VHZmdyZk8waDdtQ21ZUG1TMV9LdFAtbEpkdXhqTzZXNXhHcjFJZl9FLWp3TUhCTnlKTlU5b1VsMWkwOXBmT0NZZ3dSNnJ3YlFKOFNuS3pqUEV2bTRJVTNIX2NTcFlyWVRlZUlNanEtYkppLXRJVWFFOWN0a2VZZ09rR0g2eXVGQ0pvVU9oSVJodzNPbUN4TmJIN1FnZHZMbnR2cXFDZjBQTXdnQ1BUTkdmSGRWcC01aG1YcVdsTFdkZlV3Y1ZRdy1fLWVN\",\"email\":\"foo@redhat.com\"}}}"
	validConfigJSONButNOTBR := "{\"auths\":{\"registry.svc.ci.openshift.org\":{\"auth\":\"dXNlcjozdlpKWmVjNWZiWmZOS3hTNUVsek96Q3M2VlWTBSb0ZWTWs3MWRTejRr\"},\"cloud.openshift.com\":{\"auth\":\"b3BlbnNoaWZ0LXJlbGVhc2UtV2K2dtb250ZXJvcmVkaGF0Y29tMWhycWFjY2NudXE5NHlwd2cyMG0weXBhbTNqOkJOSzlWVU01N01CVlMyWFg1NEZRQU1ZQjZSUzdHSk5WUVhIOExEQlhOUU9NSU9DMTJKRFNJVFY1NTRNVVJDRlI=\",\"email\":\"foo@redhat.com\"},\"quay.io\":{\"auth\":\"b3BlbnNoaWLXJlbGVhc2UtZGV2K2dtb250ZXJvcmVkaGF0Y29tMWhycWFjY2NudXE5NHlwd2cyMG0weXBhbTNqOkJOSzlWVU01N01CVlMyWFg1NEZRQU1ZQjZSUzdHSk5WUVhIOExEQlhOUU9NSU9DMTJKRFNJVFY1NTRNVVJDRlI=\",\"email\":\"foo@redhat.com\"},\"registry.connect.redhat.com\":{\"auth\":\"NzY3OTcyNHx1aGMUhScWFjQ2NOVVE5NFlQd2cyMG0weXBhTTNqOmV5SmhiR2NpT2lKU1V6VXhNaUo5LmV5SnpkV0lpT2lJMVlUTm1OMkkyTW1ZME5HSTBaVGM1WVROaFlqYzRZVEZrTURFMFlqRTRNeUo5LkY4d3ZXM052dmxPcHZDRVM1RTF5R25BRjNHcEwwOGhOZmF2YnlmbzBIVXAwZ0x2VXcxNVhFR3VVZzd6RVdtVmdZeExmZDlnUTFJc243R1ZocG4wN1RBNlByQ3p3OXMyYjVTUFBoZkEtamp2ZjRWdHktTHNnaHIzODRQTlJJa0lhWElPVGlsVkJId1lpVmw1TXRUSW1nbnVCOXN3RXc5RDJVMnRKZEk5RDNDTm9zVzNTZGl0WFI5S01UQXdQNm1MZDNGbWhzTUV0eGRjVGMyQTk2bGM5UWp2cFJqUHhJUXNDSDBLRzV0OF9UaU5OQUFoV19YWTYyaFZPSS1ya0lhZV9BMjhSa2hFdzNzRFVYTElMTHFoRmZqUFB3dVV0SjVUS3FSWE80Z0I0TUhMU2VOdm9Fa0Y0dloyZmRKZVRwR0R0SmZMOUgxbGI2LWNraVVDbjh3MFB6TmRmTURIaC1pNUpNNXFIOW9CbHBBSjFpcnItQlhBdWExUEtaNnVhQVZnQ25DR2I2d3JQbHVnRlp0T1B2V0dpeTRTdm5LNk9kenBPVF9LMElmNS1MSXdkM21RLXVhQlN3Uy1OVFVvcjNTcFZud0ZzNDlJblRuTC1uTWVjMkppcDdwdnhFdWdwTWk0anQ1X2plc0VHZmdyZk8waDdtQ21ZUG1TMV9LdFAtbEpkdXhqTzZXNXhHcjFJZl9FLWp3TUhCTnlKTlU5b1VsMWkwOXBmT0NZZ3dSNnJ3YlFKOFNuS3pqUEV2bTRJVTNIX2NTcFlyWVRlZUlNanEtYkppLXRJVWFFOWN0a2VZZ09rR0g2eXVGQ0pvVU9oSVJodzNPbUN4TmJIN1FnZHZMbnR2cXFDZjBQTXdnQ1BUTkdmSGRWcC01aG1YcVdsTFdkZlV3Y1ZRdy1fLWVN\",\"email\":\"foo@redhat.com\"}}}"
	for _, tt := range []struct {
		name string
		// went per line vs. a block of expected test in case assumptions on ordering are subverted, as well as defer on
		// getting newlines right
		expectedResponse []string
		secretLister     *fakeSecretLister
		configLister     *fakeConfigLister
		configMapLister  *fakeConfigMapLister
	}{
		{
			name: "all good",
			expectedResponse: []string{
				"# HELP openshift_samples_failed_imagestream_import_info Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
				"# TYPE openshift_samples_failed_imagestream_import_info gauge",
				"openshift_samples_failed_imagestream_import_info{name=\"bar\"} 0",
				"openshift_samples_failed_imagestream_import_info{name=\"foo\"} 0",
				"# HELP openshift_samples_invalidsecret_info Indicates if the install pull secret is valid for pulling images from registry.redhat.io.  If invalid, the reason supplied either points to it being inaccessible, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
				"# TYPE openshift_samples_invalidsecret_info gauge",
				"openshift_samples_invalidsecret_info{reason=\"missing_secret\"} 0",
				"openshift_samples_invalidsecret_info{reason=\"missing_tbr_credential\"} 0",
			},
			configMapLister: &fakeConfigMapLister{},
			secretLister: &fakeSecretLister{
				s: &corev1.Secret{
					Data: map[string][]byte{
						".dockerconfigjson": []byte(validTBRCred),
					},
				},
			},
			configLister: &fakeConfigLister{
				c: &configv1.Config{
					Status: configv1.ConfigStatus{
						ManagementState: operatorv1.Managed,
						Conditions: []configv1.ConfigCondition{
							{
								Type:   configv1.ImportImageErrorsExist,
								Status: corev1.ConditionFalse,
							},
							{
								Type:   configv1.ConfigurationValid,
								Status: corev1.ConditionTrue,
							},
							{
								Type:   configv1.ImportCredentialsExist,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
		},
		{
			name: "valid config json but missing tbr",
			expectedResponse: []string{
				"# HELP openshift_samples_failed_imagestream_import_info Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
				"# TYPE openshift_samples_failed_imagestream_import_info gauge",
				"openshift_samples_failed_imagestream_import_info{name=\"bar\"} 0",
				"openshift_samples_failed_imagestream_import_info{name=\"foo\"} 0",
				"# HELP openshift_samples_invalidsecret_info Indicates if the install pull secret is valid for pulling images from registry.redhat.io.  If invalid, the reason supplied either points to it being inaccessible, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
				"# TYPE openshift_samples_invalidsecret_info gauge",
				"openshift_samples_invalidsecret_info{reason=\"missing_secret\"} 0",
				"openshift_samples_invalidsecret_info{reason=\"missing_tbr_credential\"} 1",
			},
			configMapLister: &fakeConfigMapLister{},
			secretLister: &fakeSecretLister{
				s: &corev1.Secret{
					Data: map[string][]byte{
						".dockerconfigjson": []byte(validConfigJSONButNOTBR),
					},
				},
			},
			configLister: &fakeConfigLister{
				c: &configv1.Config{
					Status: configv1.ConfigStatus{
						ManagementState: operatorv1.Managed,
						Conditions: []configv1.ConfigCondition{
							{
								Type:   configv1.ImportImageErrorsExist,
								Status: corev1.ConditionFalse,
							},
							{
								Type:   configv1.ConfigurationValid,
								Status: corev1.ConditionTrue,
							},
							{
								Type:   configv1.ImportCredentialsExist,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
		},
		{
			name: "missing config json",
			expectedResponse: []string{
				"# HELP openshift_samples_failed_imagestream_import_info Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
				"# TYPE openshift_samples_failed_imagestream_import_info gauge",
				"openshift_samples_failed_imagestream_import_info{name=\"bar\"} 0",
				"openshift_samples_failed_imagestream_import_info{name=\"foo\"} 0",
				"# HELP openshift_samples_invalidsecret_info Indicates if the install pull secret is valid for pulling images from registry.redhat.io.  If invalid, the reason supplied either points to it being inaccessible, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
				"# TYPE openshift_samples_invalidsecret_info gauge",
				"openshift_samples_invalidsecret_info{reason=\"missing_secret\"} 0",
				"openshift_samples_invalidsecret_info{reason=\"missing_tbr_credential\"} 1",
			},
			configMapLister: &fakeConfigMapLister{},
			secretLister: &fakeSecretLister{
				s: &corev1.Secret{
					Data: map[string][]byte{},
				},
			},
			configLister: &fakeConfigLister{
				c: &configv1.Config{
					Status: configv1.ConfigStatus{
						ManagementState: operatorv1.Managed,
						Conditions: []configv1.ConfigCondition{
							{
								Type:   configv1.ImportImageErrorsExist,
								Status: corev1.ConditionFalse,
							},
							{
								Type:   configv1.ConfigurationValid,
								Status: corev1.ConditionTrue,
							},
							{
								Type:   configv1.ImportCredentialsExist,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
		},
		{
			name: "missing pull secret",
			expectedResponse: []string{
				"# HELP openshift_samples_failed_imagestream_import_info Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
				"# TYPE openshift_samples_failed_imagestream_import_info gauge",
				"openshift_samples_failed_imagestream_import_info{name=\"bar\"} 0",
				"openshift_samples_failed_imagestream_import_info{name=\"foo\"} 0",
				"# HELP openshift_samples_invalidsecret_info Indicates if the install pull secret is valid for pulling images from registry.redhat.io.  If invalid, the reason supplied either points to it being inaccessible, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
				"# TYPE openshift_samples_invalidsecret_info gauge",
				"openshift_samples_invalidsecret_info{reason=\"missing_secret\"} 1",
				"openshift_samples_invalidsecret_info{reason=\"missing_tbr_credential\"} 1",
			},
			configMapLister: &fakeConfigMapLister{},
			secretLister: &fakeSecretLister{
				s: nil,
			},
			configLister: &fakeConfigLister{
				c: &configv1.Config{
					Status: configv1.ConfigStatus{
						ManagementState: operatorv1.Managed,
						Conditions: []configv1.ConfigCondition{
							{
								Type:   configv1.ImportImageErrorsExist,
								Status: corev1.ConditionFalse,
							},
							{
								Type:   configv1.ConfigurationValid,
								Status: corev1.ConditionTrue,
							},
							{
								Type:   configv1.ImportCredentialsExist,
								Status: corev1.ConditionFalse,
							},
						},
					},
				},
			},
		},
		{
			name: "bad imports",
			expectedResponse: []string{
				"# HELP openshift_samples_failed_imagestream_import_info Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
				"# TYPE openshift_samples_failed_imagestream_import_info gauge",
				"openshift_samples_failed_imagestream_import_info{name=\"bar\"} 1",
				"openshift_samples_failed_imagestream_import_info{name=\"foo\"} 1",
				"# HELP openshift_samples_invalidsecret_info Indicates if the install pull secret is valid for pulling images from registry.redhat.io.  If invalid, the reason supplied either points to it being inaccessible, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
				"# TYPE openshift_samples_invalidsecret_info gauge",
				"openshift_samples_invalidsecret_info{reason=\"missing_secret\"} 0",
				"openshift_samples_invalidsecret_info{reason=\"missing_tbr_credential\"} 0",
			},
			secretLister: &fakeSecretLister{
				s: &corev1.Secret{
					Data: map[string][]byte{
						".dockerconfigjson": []byte(validTBRCred),
					},
				},
			},
			configMapLister: &fakeConfigMapLister{
				cms: []*corev1.ConfigMap{
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name: "bar",
						},
						Data: map[string]string{"bar": "could not import"},
					},
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Data: map[string]string{"foo": "could not import"},
					},
				},
			},
			configLister: &fakeConfigLister{
				c: &configv1.Config{
					Status: configv1.ConfigStatus{
						ManagementState: operatorv1.Managed,
						Conditions: []configv1.ConfigCondition{
							{
								Type:   configv1.ImportImageErrorsExist,
								Status: corev1.ConditionTrue,
								Reason: "",
							},
							{
								Type:   configv1.ConfigurationValid,
								Status: corev1.ConditionTrue,
							},
							{
								Type:   configv1.ImportCredentialsExist,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
		},
	} {
		registry := prometheus.NewRegistry()

		sc := samplesCollector{
			secrets:    tt.secretLister,
			config:     tt.configLister,
			configmaps: tt.configMapLister,
		}

		registry.MustRegister(&sc)

		h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.PanicOnError})
		rw := &fakeResponseWriter{header: http.Header{}}
		h.ServeHTTP(rw, &http.Request{})

		respStr := rw.String()

		for _, s := range tt.expectedResponse {
			if !strings.Contains(respStr, s) {
				t.Errorf("testcase %s: expected string %s did not appear in %s", tt.name, s, respStr)
			}
		}
	}

}
