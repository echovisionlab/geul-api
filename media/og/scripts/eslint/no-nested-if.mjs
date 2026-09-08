const functionNodeTypes = new Set([
  'ArrowFunctionExpression',
  'FunctionDeclaration',
  'FunctionExpression',
]);

export const noNestedIf = {
  meta: {
    type: 'suggestion',
    docs: {
      description: 'Require guard clauses or extracted functions instead of nested if statements',
    },
    schema: [],
    messages: {
      nested: 'Avoid nested if statements; use a guard clause or extract the inner decision.',
    },
  },
  create(context) {
    return {
      IfStatement(node) {
        const ancestors = context.sourceCode.getAncestors(node);
        let descendant = node;
        for (let index = ancestors.length - 1; index >= 0; index -= 1) {
          const ancestor = ancestors[index];
          if (functionNodeTypes.has(ancestor.type)) {
            return;
          }
          if (ancestor.type !== 'IfStatement') {
            continue;
          }
          if (ancestor.alternate === descendant) {
            descendant = ancestor;
            continue;
          }
          context.report({ node, messageId: 'nested' });
          return;
        }
      },
    };
  },
};
