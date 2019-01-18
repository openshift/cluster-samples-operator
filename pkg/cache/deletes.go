package cache

import (
	"sync"
)

var (
	streamDeletesLock      = sync.Mutex{}
	imagestreamMassDeletes = map[string]bool{}
	templateDeletesLock    = sync.Mutex{}
	templateMassDeletes    = map[string]bool{}
)

// filter the first delete event after a mass delete (as we immediately recreate after a mass delete)

func ImageStreamDeletePartOfMassDelete(key string) bool {
	streamDeletesLock.Lock()
	defer streamDeletesLock.Unlock()
	_, ok := imagestreamMassDeletes[key]
	delete(imagestreamMassDeletes, key)
	return ok
}

func ImageStreamMassDeletesAdd(key string) {
	streamDeletesLock.Lock()
	defer streamDeletesLock.Unlock()
	imagestreamMassDeletes[key] = true
}

func TemplateDeletePartOfMassDelete(key string) bool {
	templateDeletesLock.Lock()
	defer templateDeletesLock.Unlock()
	_, ok := templateMassDeletes[key]
	delete(templateMassDeletes, key)
	return ok
}

func TemplateMassDeletesAdd(key string) {
	templateDeletesLock.Lock()
	defer templateDeletesLock.Unlock()
	templateMassDeletes[key] = true
}
