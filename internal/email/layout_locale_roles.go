package email

// ExtractLayoutStoredLocaleValues decodes locale-owned values without relying
// on the row's current source/target role. Sparse overlays expose only marked
// values; a canonical row that used to be the source exposes every source unit
// as an explicit value when that row later becomes a target.
func ExtractLayoutStoredLocaleValues(content string) (map[string]string, error) {
	_, hasTargetMarkers, err := LayoutMarkerPresence(content)
	if err != nil {
		return nil, err
	}
	if hasTargetMarkers {
		return ExtractLayoutLocaleValues(content)
	}
	units, err := ExtractLayoutContentUnits(content)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(units))
	for _, unit := range units {
		values[unit.Handle] = unit.SourceValue
	}
	return values, nil
}

// MaterializeLayoutSourceLocale returns a canonical source-role wrapper from
// either persisted representation. A sparse overlay keeps its explicit values
// and represents omitted source-role units as empty without losing their unit
// markers.
func MaterializeLayoutSourceLocale(content string) (*string, *string, error) {
	_, hasTargetMarkers, err := LayoutMarkerPresence(content)
	if err != nil {
		return nil, nil, err
	}
	if !hasTargetMarkers {
		if _, err := ExtractLayoutContentUnits(content); err != nil {
			return nil, nil, err
		}
		text := StripHTML(content)
		return stringPointer(content), stringPointer(text), nil
	}
	structure, err := stripLayoutValueMarkers(content)
	if err != nil {
		return nil, nil, err
	}
	return MaterializeLayoutSourceFromLocale(structure, &content)
}

// MaterializeLayoutSourceFromLocale applies one stored locale's values to the
// current shared source structure. Missing values become marker-preserving
// empty source units; values unknown to the current structure are ignored.
func MaterializeLayoutSourceFromLocale(
	currentSource string,
	storedLocale *string,
) (*string, *string, error) {
	units, err := ExtractLayoutContentUnits(currentSource)
	if err != nil {
		return nil, nil, err
	}
	values := make(map[string]string, len(units))
	if storedLocale != nil {
		values, err = ExtractLayoutStoredLocaleValues(*storedLocale)
		if err != nil {
			return nil, nil, err
		}
	}
	complete := make(map[string]string, len(units))
	for _, unit := range units {
		complete[unit.Handle] = values[unit.Handle]
	}
	return ApplyLayoutSourceValues(currentSource, complete)
}

// EmptyLayoutLocaleFromSource creates the values-only row required when a
// missing locale becomes the source. Every current unit is explicitly empty so
// a later role inversion retains empty rather than source fallback semantics.
func EmptyLayoutLocaleFromSource(source string) (*string, *string, error) {
	units, err := ExtractLayoutContentUnits(source)
	if err != nil {
		return nil, nil, err
	}
	values := make(map[string]string, len(units))
	for _, unit := range units {
		values[unit.Handle] = ""
	}
	return ApplyLayoutLocaleValues(source, values)
}

// NormalizeLayoutLocaleRepresentation re-embeds existing locale values in the
// current source structure without changing absent/explicit-empty semantics.
func NormalizeLayoutLocaleRepresentation(
	currentSource string,
	storedLocale string,
) (*string, *string, error) {
	values, err := ExtractLayoutStoredLocaleValues(storedLocale)
	if err != nil {
		return nil, nil, err
	}
	return ApplyLayoutLocaleValues(currentSource, values)
}
