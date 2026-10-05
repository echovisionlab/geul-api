import { context, isSpanContextValid, trace } from '@opentelemetry/api';
import { logs, SeverityNumber, type AnyValueMap } from '@opentelemetry/api-logs';
import {
  normalizeLogAttributes,
  SERVICE_OG,
  type SystemLogLevel,
  type SystemRecord,
} from '@echovisionlab/geul-telemetry';
import pino, { type LoggerOptions } from 'pino';

const otelLogger = logs.getLogger(SERVICE_OG);
const typedSystemRecord = Symbol('typedSystemRecord');
const genericControlFields = [
  'event',
  'action',
  'outcome',
  'request_id',
  'trace_id',
  'span_id',
] as const;

function messageFromArguments(args: unknown[]): string {
  if (typeof args[0] === 'string') return args[0];
  if (typeof args[1] === 'string') return args[1];
  return 'application log';
}

function removeGenericControlFields(
  source: Readonly<Record<string, unknown>> | undefined,
  attributes: Record<string, string | number | boolean>,
): void {
  if ((source as Readonly<Record<PropertyKey, unknown>> | undefined)?.[typedSystemRecord] === true) {
    return;
  }
  for (const field of genericControlFields) {
    delete attributes[field];
  }
}

function normalizeLogArguments(
  args: unknown[],
  message: string
): Record<string, string | number | boolean> {
  const first = args[0];
  const source = typeof first === 'object' && first !== null && !(first instanceof Error)
    ? first as Readonly<Record<string, unknown>>
    : first instanceof Error
      ? { error: first }
      : undefined;
  const attributes = normalizeLogAttributes(source);
  removeGenericControlFields(source, attributes);
  if (source !== undefined && !(first instanceof Error)) {
    args[0] = attributes;
  } else if (first instanceof Error) {
    args.splice(0, args.length, attributes, message);
  } else {
    args.unshift(attributes);
  }
  return attributes;
}

function severityForLevel(level: number): { number: SeverityNumber; text: string } {
  if (level >= 60) return { number: SeverityNumber.FATAL, text: 'FATAL' };
  if (level >= 50) return { number: SeverityNumber.ERROR, text: 'ERROR' };
  if (level >= 40) return { number: SeverityNumber.WARN, text: 'WARN' };
  if (level >= 30) return { number: SeverityNumber.INFO, text: 'INFO' };
  return { number: SeverityNumber.DEBUG, text: 'DEBUG' };
}

function applyTraceContext(attributes: Record<string, unknown>): void {
  const spanContext = trace.getSpan(context.active())?.spanContext();
  if (spanContext && isSpanContextValid(spanContext)) {
    attributes.trace_id ??= spanContext.traceId;
    attributes.span_id ??= spanContext.spanId;
  }
}

function emitOpenTelemetryLog(
  attributes: Record<string, unknown>,
  message: string,
  severity: { number: SeverityNumber; text: string }
): void {
  otelLogger.emit({
    timestamp: Date.now(),
    severityNumber: severity.number,
    severityText: severity.text,
    eventName: typeof attributes.event === 'string' ? attributes.event : undefined,
    body: message,
    attributes: attributes as AnyValueMap,
    context: context.active(),
  });
}

const loggerOptions: LoggerOptions = {
  level: process.env.LOG_LEVEL || 'info',
  timestamp: pino.stdTimeFunctions.isoTime,
  formatters: {
    level: (label) => ({ level: label.toUpperCase() }),
  },
  hooks: {
    logMethod(args, method, level) {
      const message = messageFromArguments(args);
      const attributes = normalizeLogArguments(args, message);
      const severity = severityForLevel(level ?? 30);
      applyTraceContext(attributes);
      emitOpenTelemetryLog(attributes, message, severity);
      method.apply(this, args);
    },
  },
};

const pinoLogger = pino(loggerOptions);

export const logger = Object.assign(pinoLogger, {
  system(level: SystemLogLevel, record: SystemRecord): void {
    pinoLogger[level]({ ...record, [typedSystemRecord]: true }, 'System event');
  },
});
