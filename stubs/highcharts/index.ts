type PlainObject = Record<string, any>;

function isPlainObject(value: unknown): value is PlainObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function mergeInto(target: PlainObject, source: PlainObject): PlainObject {
  for (const [key, value] of Object.entries(source)) {
    if (Array.isArray(value)) {
      target[key] = value.slice();
      continue;
    }
    if (isPlainObject(value)) {
      const existing = isPlainObject(target[key]) ? target[key] : {};
      target[key] = mergeInto({ ...existing }, value);
      continue;
    }
    target[key] = value;
  }
  return target;
}

function merge(...inputs: any[]): any {
  const out: PlainObject = {};
  for (const input of inputs) {
    if (!isPlainObject(input)) continue;
    mergeInto(out, input);
  }
  return out;
}

function addEvent(_target: any, _eventName: string, _handler: (...args: any[]) => void): () => void {
  return () => {};
}

const Highcharts: any = {
  merge,
  addEvent,
};

export default Highcharts;
