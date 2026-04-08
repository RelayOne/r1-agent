The file `main.go` contains two structs, `FileStore` and `DBStore`, that both implement `Save(key string, data []byte) error` and `Load(key string) ([]byte, error)` methods. They share a common pattern but there is no interface unifying them.

Extract a `Store` interface with `Save` and `Load` methods. Then add a function `CopyData(src Store, dst Store, key string) error` that loads data from the source store and saves it to the destination store. Do not remove or change the existing structs or their methods.
