package prometheus

import (
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	v1alpha1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1alpha1"
	alertmanagertypes "github.com/prometheus/alertmanager/types"

	"github.com/google/go-cmp/cmp"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestRawAlertResource_ToAlerts(t *testing.T) {
	type fields struct {
		AlertmanagerConfig *v1alpha1.AlertmanagerConfig
		PrometheusRule     *monitoringv1.PrometheusRule
		Silences           []alertmanagertypes.Silence
	}
	type args struct {
		containOrigin bool
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []MonitorAlertRule
		wantErr bool
	}{
		{
			name: "1",
			fields: fields{
				AlertmanagerConfig: &v1alpha1.AlertmanagerConfig{
					ObjectMeta: v1.ObjectMeta{
						Name:      "myconfig",
						Namespace: GlobalAlertNamespace,
					},
					Spec: v1alpha1.AlertmanagerConfigSpec{
						Receivers: []v1alpha1.Receiver{
							NullReceiver,
						},
						Route: &v1alpha1.Route{
							Receiver: NullReceiverName,
							Routes: []extv1.JSON{
								{
									Raw: []byte(`
									{
										"receiver": "receiver-1",
										"matchers": [
											{
												"name": "gems_alertname",
												"value": "alert-1"
											},
											{
												"name": "gems_namespace",
												"value": "gemcloud-monitoring-system"
											}
										]
									}`),
								},
							},
						},
					},
				},
				PrometheusRule: &monitoringv1.PrometheusRule{
					ObjectMeta: v1.ObjectMeta{
						Name:      "myrule",
						Namespace: GlobalAlertNamespace,
					},
					Spec: monitoringv1.PrometheusRuleSpec{
						Groups: []monitoringv1.RuleGroup{
							{
								Name: "alert-1",
								Rules: []monitoringv1.Rule{
									{
										Alert: "alert-1",
										Expr:  intstr.FromString(`kube_node_status_condition{condition=~"Ready", status=~"true"} == 0`),
										For:   "1m",
										Labels: map[string]string{
											AlertNameLabel:      "alert-1",
											AlertNamespaceLabel: GlobalAlertNamespace,
											SeverityLabel:       SeverityError,
										},
										Annotations: map[string]string{
											exprJsonAnnotationKey: `{
												"resource": "node",
												"rule": "statusCondition",
												"unit": "",
												"labelpairs": {
													"condition": "Ready",
													"status": "true"
												},
												"compareOp": "==",
												"compareValue": "0"
											}`,
										},
									},
								},
							},
						},
					},
				},
				Silences: nil,
			},
			args: args{
				containOrigin: false,
			},
			want: []MonitorAlertRule{{
				BaseAlertRule: BaseAlertRule{
					Namespace: GlobalAlertNamespace,
					Name:      "alert-1",
					For:       "1m",
					AlertLevels: []AlertLevel{{
						CompareOp:    "==",
						CompareValue: "0",
						Severity:     SeverityError,
					}},
					Receivers: []AlertReceiver{{Name: "receiver-1"}},
					IsOpen:    true,
				},

				BaseQueryParams: BaseQueryParams{
					Resource: "node",
					Rule:     "statusCondition",
					Unit:     "",
					LabelPairs: map[string]string{
						"condition": "Ready",
						"status":    "true",
					},
				},

				Promql: `kube_node_status_condition{condition=~"Ready", status=~"true"}`,
			}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := &RawMonitorAlertResource{
				Base: &BaseAlertResource{
					AMConfig: tt.fields.AlertmanagerConfig,
					Silences: tt.fields.Silences,
				},
				PrometheusRule: tt.fields.PrometheusRule,
				MonitorOptions: DefaultMonitorOptions(),
			}
			got, err := raw.ToAlerts(tt.args.containOrigin)
			if (err != nil) != tt.wantErr {
				t.Errorf("RawAlertResource.ToAlerts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			for i := range got {
				got[i].RuleContext = RuleContext{}
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("RawAlertResource.ToAlerts() = %v, want %v", got, tt.want)
				t.Error("diff: ", diff)
			}
		})
	}
}