package entityparticipant

// Related is a row of another domain that references an Artist or a Label,
// as a deletion preview names it. RelationCount is how many relations of the
// row point at the entity.
type Related struct {
	ID            string
	DisplayName   string
	RelationCount int32
}
