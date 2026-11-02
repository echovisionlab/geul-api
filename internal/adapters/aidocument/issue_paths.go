package aidocumentadapter

import core "github.com/echovisionlab/geul-api/internal/aidocument"

// Compiler diagnostics retain their established target paths. Mutation
// acknowledgements and revision conflicts use the core's affected handles.
func operationIssuePath(operation core.Operation) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{string(operation.SetField.Target.Block), string(operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{string(operation.UnsetField.Target.Block), string(operation.UnsetField.Target.Field)}
	case core.OperationInsertBlock:
		return []string{string(operation.InsertBlock.Block)}
	case core.OperationDeleteBlock:
		return []string{string(operation.DeleteBlock.Block)}
	case core.OperationMoveBlock:
		return []string{string(operation.MoveBlock.Block)}
	case core.OperationReplaceBlockKind:
		return []string{string(operation.ReplaceBlockKind.Block)}
	case core.OperationAttachFile:
		return []string{string(operation.AttachFile.Target.Block), string(operation.AttachFile.Target.Field), string(operation.AttachFile.File)}
	case core.OperationDetachFile:
		return []string{string(operation.DetachFile.Target.Block), string(operation.DetachFile.Target.Field)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"locale"}
	default:
		return nil
	}
}

func compactIssueHandle(operation core.Operation, locale core.Locale, fallback string) string {
	switch operation.Kind {
	case core.OperationSetField:
		return "block:" + string(operation.SetField.Target.Block) + "/field:" + string(operation.SetField.Target.Field)
	case core.OperationUnsetField:
		return "block:" + string(operation.UnsetField.Target.Block) + "/field:" + string(operation.UnsetField.Target.Field)
	case core.OperationInsertBlock:
		return "block:" + string(operation.InsertBlock.Block)
	case core.OperationDeleteBlock:
		return "block:" + string(operation.DeleteBlock.Block)
	case core.OperationMoveBlock:
		return "block:" + string(operation.MoveBlock.Block)
	case core.OperationReplaceBlockKind:
		return "block:" + string(operation.ReplaceBlockKind.Block)
	case core.OperationAttachFile:
		return "block:" + string(operation.AttachFile.Target.Block) + "/field:" + string(operation.AttachFile.Target.Field)
	case core.OperationDetachFile:
		return "block:" + string(operation.DetachFile.Target.Block) + "/field:" + string(operation.DetachFile.Target.Field)
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return "translation:" + string(locale)
	default:
		return fallback
	}
}
