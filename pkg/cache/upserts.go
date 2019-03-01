package cache

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"

	imagev1 "github.com/openshift/api/image/v1"
)

var (
	upsertsLock = sync.Mutex{}
	upserts     = map[string]*imagev1.ImageStream{}
	timeout     kapis.Time
)

func AddUpsert(key string) {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	if len(upserts) == 0 {
		timeout = kapis.Now()
	}
	upserts[key] = nil
}

func RemoveUpsert(key string) {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	delete(upserts, key)
}

func AllUpsertEventsArrived() bool {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	now := kapis.Now()
	for key, stream := range upserts {
		if stream == nil {
			if now.Sub(timeout.Time) > 5*time.Minute {
				// if we have not retrieved all the events
				// within 5 minutes of the start of recording,
				// let's move on
				logrus.Printf("have not received an upsert event for %s in 5 minutes so cache is skipping this one", key)
				continue
			}
			logrus.Printf("have not received an upsert event for imagestream %s", key)
			return false
		}
	}
	return true
}

func AddReceivedEventFromUpsert(stream *imagev1.ImageStream) {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	upserts[stream.Name] = stream
}

func GetUpsertImageStreams() map[string]*imagev1.ImageStream {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	ret := map[string]*imagev1.ImageStream{}
	for key, stream := range upserts {
		ret[key] = stream
	}
	return ret
}

func ClearUpsertsCache() {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	upserts = map[string]*imagev1.ImageStream{}
}

func UpsertsAmount() int {
	upsertsLock.Lock()
	defer upsertsLock.Unlock()
	return len(upserts)
}
