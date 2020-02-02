package kotsadm

import (
	"github.com/replicatedhq/kots/pkg/kotsadm/hostnetwork"
	"os"

	"github.com/replicatedhq/kots/pkg/kotsadm/types"
	"github.com/replicatedhq/kots/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var postgresPVSize = resource.MustParse("1Gi")

func postgresStatefulset(deployOptions types.DeployOptions) *appsv1.StatefulSet {
	size := postgresPVSize

	if deployOptions.LimitRange != nil {
		var allowedMax *resource.Quantity
		var allowedMin *resource.Quantity

		for _, limit := range deployOptions.LimitRange.Spec.Limits {
			if limit.Type == corev1.LimitTypePersistentVolumeClaim {
				max, ok := limit.Max["storage"]
				if ok {
					allowedMax = &max
				}

				min, ok := limit.Min["storage"]
				if ok {
					allowedMin = &min
				}
			}
		}

		newSize := promptForSizeIfNotBetween("postgres", &size, allowedMin, allowedMax)
		if newSize == nil {
			os.Exit(-1)
		}

		size = *newSize
	}

	var securityContext corev1.PodSecurityContext
	if !deployOptions.IsOpenShift {
		securityContext = corev1.PodSecurityContext{
			RunAsUser: util.IntPointer(999),
			FSGroup:   util.IntPointer(999),
		}
	}

	statefulset := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kotsadm-postgres",
			Namespace: deployOptions.Namespace,
			Labels: map[string]string{
				types.KotsadmKey: types.KotsadmLabelValue,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "kotsadm-postgres",
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kotsadm-postgres",
						Labels: map[string]string{
							types.KotsadmKey: types.KotsadmLabelValue,
						},
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceName(corev1.ResourceStorage): size,
							},
						},
					},
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":            "kotsadm-postgres",
						types.KotsadmKey: types.KotsadmLabelValue,
					},
				},
				Spec: corev1.PodSpec{
					SecurityContext: &securityContext,
					Volumes: []corev1.Volume{
						{
							Name: "kotsadm-postgres",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "kotsadm-postgres",
								},
							},
						},
					},
					HostNetwork: deployOptions.HostNetwork,
					Tolerations: hostnetwork.Tolerations(deployOptions.HostNetwork),
					Containers: []corev1.Container{
						{
							Image:           "postgres:10.7",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "kotsadm-postgres",
							Ports: []corev1.ContainerPort{
								{
									Name:          "postgres",
									ContainerPort: 5432,
									HostPort:      hostnetwork.MaybeHostPortMap(deployOptions.HostNetwork).PostgresPostgres,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "kotsadm-postgres",
									MountPath: "/var/lib/postgresql/data",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "PGDATA",
									Value: "/var/lib/postgresql/data/pgdata",
								},
								{
									Name:  "POSTGRES_USER",
									Value: "kotsadm",
								},
								{
									Name: "POSTGRES_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "kotsadm-postgres",
											},
											Key: "password",
										},
									},
								},
								{
									Name:  "POSTGRES_DB",
									Value: "kotsadm",
								},
							},
							LivenessProbe: &corev1.Probe{
								InitialDelaySeconds: 30,
								TimeoutSeconds:      5,
								FailureThreshold:    3,
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/bin/sh",
											"-i",
											"-c",
											"pg_isready -U kotsadm -h 127.0.0.1 -p 5432",
										},
									},
								},
							},
							ReadinessProbe: &corev1.Probe{
								InitialDelaySeconds: 1,
								PeriodSeconds:       1,
								TimeoutSeconds:      1,
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/bin/sh",
											"-i",
											"-c",
											"pg_isready -U kotsadm -h 127.0.0.1 -p 5432",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return statefulset
}

func postgresService(namespace string) *corev1.Service {
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kotsadm-postgres",
			Namespace: namespace,
			Labels: map[string]string{
				types.KotsadmKey: types.KotsadmLabelValue,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": "kotsadm-postgres",
			},
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name:       "postgres",
					Port:       5432,
					TargetPort: intstr.FromString("postgres"),
				},
			},
		},
	}

	return service
}

// this is a pretty egregious hack to enable development against
// clusters without storage classes ready. This is primarily for
// delivering/managing CNI impls with kotsadm, since any non-cloud-specific
// volume provisioner (e.g. rook/ceph, gluster) will probably require
// a functioning pod network.
//
// Repeat: using a throwaway PV is really dangerous, but it enables
// us to get kots up and running on a single node without a pod network.
//
// somebody please make this better :)
func postgresHostpathVolume() *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolume",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "kotsadm-postgres-host-pv",
			Labels: map[string]string{
				types.KotsadmKey: types.KotsadmLabelValue,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: postgresPVSize,
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/opt/kotsadm/postgres-pv",
				},
			},
		},
	}
}
