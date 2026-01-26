// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"strings"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ByoAdmissionReconciler reconciles a ByoAdmission object
type ByoAdmissionReconciler struct {
	ClientSet clientset.Interface
}

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kubelet-serving,verbs=approve

// Reconcile continuously checks for CSRs and approves them
func (r *ByoAdmissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received", "object", req.NamespacedName)

	// Fetch the CSR from the api-server
	csr, err := r.ClientSet.CertificatesV1().CertificateSigningRequests().Get(ctx, req.NamespacedName.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "CertificateSigningRequest not found, won't reconcile")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Check if the CSR is already approved or denied
	csrApproved := checkCSRCondition(csr.Status.Conditions, certv1.CertificateApproved)
	csrDenied := checkCSRCondition(csr.Status.Conditions, certv1.CertificateDenied)
	if csrApproved || csrDenied {
		if csrApproved {
			logger.Info("CertificateSigningRequest is already approved", "CSR", csr.Name)
		}
		if csrDenied {
			logger.Info("CertificateSigningRequest is already denied", "CSR", csr.Name)
		}
		return ctrl.Result{}, nil
	}

	// Approve CSR based on signer type
	switch csr.Spec.SignerName {
	case certv1.KubeAPIServerClientSignerName:
		// Approve BYOH client certificates (byoh-csr-* format)
		if !strings.HasPrefix(csr.Name, "byoh-csr-") {
			logger.V(4).Info("Skipping non-BYOH client CSR", "CSR", csr.Name)
			return ctrl.Result{}, nil
		}
		logger.Info("Approving BYOH client CSR", "CSR", csr.Name)

	case certv1.KubeletServingSignerName:
		// Approve kubelet serving certificates
		// Kubelet creates this CSR when using TLS Bootstrap mode
		logger.Info("Approving kubelet serving CSR", "CSR", csr.Name)

	default:
		logger.V(4).Info("Skipping CSR with unknown signer", "CSR", csr.Name, "signer", csr.Spec.SignerName)
		return ctrl.Result{}, nil
	}

	// Update the CSR to the "Approved" condition
	csr.Status.Conditions = append(csr.Status.Conditions, certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Status: corev1.ConditionTrue,
		Reason: "Approved by ByoAdmission Controller",
	})

	// Approve the CSR
	_, err = r.ClientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("CSR Approved", "object", req.NamespacedName)

	return ctrl.Result{}, nil
}

// Check if the CSR has the given condition.
func checkCSRCondition(conditions []certv1.CertificateSigningRequestCondition, conditionType certv1.RequestConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoAdmissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&certv1.CertificateSigningRequest{}).WithEventFilter(
		// Watch for BYOH client CSRs (byoh-csr-*) AND kubelet serving CSRs
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				csrName := e.Object.GetName()
				csr, ok := e.Object.(*certv1.CertificateSigningRequest)
				if !ok {
					return false
				}
				// Accept BYOH client CSRs or kubelet serving CSRs
				return strings.HasPrefix(csrName, "byoh-csr-") ||
					csr.Spec.SignerName == certv1.KubeletServingSignerName
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				csrName := e.ObjectNew.GetName()
				csr, ok := e.ObjectNew.(*certv1.CertificateSigningRequest)
				if !ok {
					return false
				}
				// Accept BYOH client CSRs or kubelet serving CSRs
				return strings.HasPrefix(csrName, "byoh-csr-") ||
					csr.Spec.SignerName == certv1.KubeletServingSignerName
			},
		}).
		Complete(r)
}
