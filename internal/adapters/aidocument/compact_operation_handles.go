package aidocumentadapter

import core "github.com/echovisionlab/geul-api/internal/aidocument"

// compactOperationHandles projects compact-profile operations into the stable
// handles used by validation results and mutation acknowledgements.
func compactOperationHandles(operation core.Operation, locale core.Locale, fallback string) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{"block:" + string(operation.SetField.Target.Block) + "/field:" + string(operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{"block:" + string(operation.UnsetField.Target.Block) + "/field:" + string(operation.UnsetField.Target.Field)}
	case core.OperationInsertBlock:
		return []string{"block:" + string(operation.InsertBlock.Block)}
	case core.OperationDeleteBlock:
		return []string{"block:" + string(operation.DeleteBlock.Block)}
	case core.OperationMoveBlock:
		return []string{"block:" + string(operation.MoveBlock.Block)}
	case core.OperationReplaceBlockKind:
		return []string{"block:" + string(operation.ReplaceBlockKind.Block)}
	case core.OperationAttachFile:
		return []string{"block:" + string(operation.AttachFile.Target.Block) + "/field:" + string(operation.AttachFile.Target.Field)}
	case core.OperationDetachFile:
		return []string{"block:" + string(operation.DetachFile.Target.Block) + "/field:" + string(operation.DetachFile.Target.Field)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"translation:" + string(locale)}
	default:
		return []string{fallback}
	}
}
