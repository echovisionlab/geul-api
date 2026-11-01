package referencecatalog

// AssetOwner is the catalog row a public asset binding belongs to.
type AssetOwner struct {
	Type string
	ID   string
}

// AssetBinding binds the ready asset of kind Kind that SourceFileID produced
// to Owner under Key.
type AssetBinding struct {
	SourceFileID string
	Owner        AssetOwner
	Key          string
	Kind         string
}

// AssetRelease releases Owner's bindings whose key starts with
// BindingPrefix.
type AssetRelease struct {
	Owner         AssetOwner
	BindingPrefix string
}

// AssetSource names the ready asset of kind Kind that FileID produced; a
// public read also accepts FallbackKinds.
type AssetSource struct {
	FileID        string
	Kind          string
	FallbackKinds []string
}
