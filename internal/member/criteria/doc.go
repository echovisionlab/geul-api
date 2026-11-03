// Package criteria owns Member criteria domain behavior and its Connect
// handler. Its use cases depend on ports: the Store (criteria/postgres in
// production, criteriatest in unit tests), authorization, the identity
// fence, audit and the recipient counter.
package criteria
