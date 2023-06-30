package k8s

import (
	"k8s.io/client-go/tools/cache"
)

// NewConfigMapStore constructs new conversionStore
func NewConfigMapStore(notifyChan chan<- struct{}) *ConfigMapStore {
	return &ConfigMapStore{
		store:         cache.NewStore(cache.MetaNamespaceKeyFunc),
		notifyChannel: notifyChan,
	}
}

var _ cache.Store = &ConfigMapStore{}

type ConfigMapStore struct {
	store         cache.Store
	notifyChannel chan<- struct{}
}

// Add adds the given object to the accumulator associated with the given object's key
func (s *ConfigMapStore) Add(obj interface{}) error {
	if err := s.store.Add(obj); err != nil {
		return err
	}
	s.notifyChannel <- struct{}{}
	return nil
}

// Update updates the given object in the accumulator associated with the given object's key
func (s *ConfigMapStore) Update(obj interface{}) error {
	if err := s.store.Update(obj); err != nil {
		return err
	}
	s.notifyChannel <- struct{}{}
	return nil
}

// Delete deletes the given object from the accumulator associated with the given object's key
func (s *ConfigMapStore) Delete(obj interface{}) error {
	if err := s.store.Delete(obj); err != nil {
		return err
	}
	s.notifyChannel <- struct{}{}
	return nil
}

// List returns a list of all the objects
func (s *ConfigMapStore) List() []interface{} {
	return s.store.List()
}

// ListKeys returns a list of all the keys
func (s *ConfigMapStore) ListKeys() []string {
	return s.store.ListKeys()
}

// Get returns the object with the given key
func (s *ConfigMapStore) Get(obj interface{}) (item interface{}, exists bool, err error) {
	return s.store.Get(obj)
}

// GetByKey returns the object with the given key
func (s *ConfigMapStore) GetByKey(key string) (item interface{}, exists bool, err error) {
	return s.store.GetByKey(key)
}

// Replace will delete the contents of the store, using instead the given list.
func (s *ConfigMapStore) Replace(list []interface{}, resourceVersion string) error {
	return s.store.Replace(list, resourceVersion)
}

// Resync invokes the cache.store Resync method
func (s *ConfigMapStore) Resync() error {
	return s.store.Resync()
}
