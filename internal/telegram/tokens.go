// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Default Secret holding the token. Hand-written descriptions use the same
// names, so an existing Secret keeps working.
const (
	DefaultSecretName = "kamerton-telegram"
	DefaultSecretKey  = "bot-token"
)

// TokenStore.Get returns "" when no token is stored.
type TokenStore interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, token string) error
	Delete(ctx context.Context) error
	Ref() (name, key string)
}

// SecretTokenStore keeps the token in one key of a Secret and preserves the other keys.
type SecretTokenStore struct {
	// Client must be uncached: RBAC allows only get/update/patch/delete of
	// this one Secret by name, not list/watch of Secrets.
	Client    client.Client
	Namespace string
	Name      string
	Key       string
	// Component labels a Secret created by the store; default "telegram".
	Component string
}

func (s *SecretTokenStore) key() types.NamespacedName {
	return types.NamespacedName{Namespace: s.Namespace, Name: s.Name}
}

func (s *SecretTokenStore) component() string {
	if s.Component == "" {
		return "telegram"
	}
	return s.Component
}

func (s *SecretTokenStore) Ref() (string, string) { return s.Name, s.Key }

func (s *SecretTokenStore) Get(ctx context.Context) (string, error) {
	var sec corev1.Secret
	if err := s.Client.Get(ctx, s.key(), &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return strings.TrimSpace(string(sec.Data[s.Key])), nil
}

func (s *SecretTokenStore) Set(ctx context.Context, token string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sec corev1.Secret
		err := s.Client.Get(ctx, s.key(), &sec)
		if apierrors.IsNotFound(err) {
			sec = corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: s.Namespace, Name: s.Name,
					Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": s.component()},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{s.Key: []byte(token)},
			}
			if err := s.Client.Create(ctx, &sec); err != nil {
				return fmt.Errorf("create Secret %s/%s: %w", s.Namespace, s.Name, sanitizeK8sErr(err))
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Secret %s/%s: %w", s.Namespace, s.Name, err)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data[s.Key] = []byte(token)
		if err := s.Client.Update(ctx, &sec); err != nil {
			if apierrors.IsConflict(err) {
				return err
			}
			return fmt.Errorf("update Secret %s/%s: %w", s.Namespace, s.Name, sanitizeK8sErr(err))
		}
		return nil
	})
}

// Delete deletes the Secret when the token is its only key, otherwise only the key.
func (s *SecretTokenStore) Delete(ctx context.Context) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sec corev1.Secret
		err := s.Client.Get(ctx, s.key(), &sec)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Secret %s/%s: %w", s.Namespace, s.Name, err)
		}
		if _, ok := sec.Data[s.Key]; ok && len(sec.Data) == 1 && len(sec.StringData) == 0 {
			if err := s.Client.Delete(ctx, &sec, client.Preconditions{ResourceVersion: &sec.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return nil
		}
		delete(sec.Data, s.Key)
		return s.Client.Update(ctx, &sec)
	})
}

// sanitizeK8sErr keeps the reason of an API error but drops the message, which
// is not under our control and could echo the object.
func sanitizeK8sErr(err error) error {
	if st, ok := err.(apierrors.APIStatus); ok {
		return fmt.Errorf("%s (HTTP %d)", st.Status().Reason, st.Status().Code)
	}
	return err
}

type MemoryTokenStore struct {
	mu    sync.Mutex
	token string
}

func (m *MemoryTokenStore) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, nil
}

func (m *MemoryTokenStore) Set(_ context.Context, t string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = t
	return nil
}

func (m *MemoryTokenStore) Delete(context.Context) error { return m.Set(context.Background(), "") }

func (m *MemoryTokenStore) Ref() (string, string) { return DefaultSecretName, DefaultSecretKey }
