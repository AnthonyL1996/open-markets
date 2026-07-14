using System;
using System.Collections.Generic;
using System.Globalization;
using System.Reflection;
using System.Text;

namespace OpenMarkets.Net
{
    /// <summary>
    /// Minimal JSON reader for the online API responses, replacing <c>UnityEngine.JsonUtility.FromJson</c> on the
    /// DESERIALIZE (incoming) path. JsonUtility (Unity 5.6 / Mono net35) silently drops an object-array field when
    /// it is mixed with scalar fields in the same JSON object: e.g. <c>/leagues/members</c>'s
    /// <c>{leagueId, name, members[]}</c> parsed <c>name</c> but left <c>members</c> empty. The same shape breaks
    /// <c>/settlements</c> (<c>events[]</c> — books cash), <c>/trades</c> (each trade's <c>items[]</c> basket) and
    /// <c>/prices</c> (<c>commodities[]</c>). This reader parses the standard JSON the Go server emits into our
    /// <c>[Serializable]</c> DTOs by FIELD NAME, so it is insensitive to key order or surrounding fields.
    ///
    /// Scope: incoming only. Outgoing request bodies still use <c>JsonUtility.ToJson</c> — serialization is
    /// unaffected by the bug and the server (encoding/json) is tolerant. Parsing never throws: malformed input or
    /// a field-type mismatch degrades to nulls/defaults so callers keep their dead-server posture. net35-safe
    /// (reflection + a hand-rolled recursive-descent parser; no LINQ-to-anything exotic, no value tuples).
    /// </summary>
    public static class OmJson
    {
        /// <summary>Parse a JSON object body into <typeparamref name="T"/>. Returns null on empty/malformed input
        /// (logged once), matching the old <c>Parse</c> contract.</summary>
        public static T Parse<T>(string json) where T : class
        {
            if (string.IsNullOrEmpty(json)) return null;
            try
            {
                object tree = new Reader(json).ParseValue();
                return Bind(typeof(T), tree) as T;
            }
            catch (Exception e)
            {
                Log.Warn("json: parse failed: " + e.Message);
                return null;
            }
        }

        // ---- reflective binder: tree (Dictionary/List/string/bool/double/long/null) -> typed object, by name ----
        private static object Bind(Type type, object node)
        {
            if (node == null) return type.IsValueType ? Activator.CreateInstance(type) : null;

            if (type == typeof(string)) return node as string ?? node.ToString();
            if (type == typeof(bool)) return ToBool(node);
            if (type == typeof(int)) return (int)ToLong(node);
            if (type == typeof(long)) return ToLong(node);
            if (type == typeof(float)) return (float)ToDouble(node);
            if (type == typeof(double)) return ToDouble(node);

            if (type.IsArray)
            {
                Type el = type.GetElementType();
                List<object> list = node as List<object>;
                if (list == null) return Array.CreateInstance(el, 0);
                Array arr = Array.CreateInstance(el, list.Count);
                for (int i = 0; i < list.Count; i++) arr.SetValue(Bind(el, list[i]), i);
                return arr;
            }

            // Custom [Serializable] DTO: map each JSON key onto the public field of the same name (order-agnostic).
            Dictionary<string, object> obj = node as Dictionary<string, object>;
            if (obj == null) return null;
            object target = Activator.CreateInstance(type);
            FieldInfo[] fields = type.GetFields(BindingFlags.Public | BindingFlags.Instance);
            for (int i = 0; i < fields.Length; i++)
            {
                object raw;
                if (!obj.TryGetValue(fields[i].Name, out raw)) continue;
                try { fields[i].SetValue(target, Bind(fields[i].FieldType, raw)); }
                catch { /* one field's type mismatch must not lose the rest of the object */ }
            }
            return target;
        }

        private static bool ToBool(object n) { return n is bool ? (bool)n : Convert.ToBoolean(n, CultureInfo.InvariantCulture); }
        private static long ToLong(object n) { return n is long ? (long)n : (n is double ? (long)(double)n : Convert.ToInt64(n, CultureInfo.InvariantCulture)); }
        private static double ToDouble(object n) { return n is double ? (double)n : (n is long ? (long)n : Convert.ToDouble(n, CultureInfo.InvariantCulture)); }

        // ---- recursive-descent reader: JSON text -> object tree. Tolerant of the well-formed JSON Go emits. ----
        private sealed class Reader
        {
            private readonly string _s;
            private int _i;
            public Reader(string s) { _s = s; _i = 0; }

            public object ParseValue()
            {
                SkipWs();
                char c = _s[_i];
                switch (c)
                {
                    case '{': return ParseObject();
                    case '[': return ParseArray();
                    case '"': return ParseString();
                    case 't': _i += 4; return true;     // "true"
                    case 'f': _i += 5; return false;    // "false"
                    case 'n': _i += 4; return null;     // "null"
                    default: return ParseNumber();
                }
            }

            private Dictionary<string, object> ParseObject()
            {
                Dictionary<string, object> d = new Dictionary<string, object>();
                _i++; // '{'
                SkipWs();
                if (_s[_i] == '}') { _i++; return d; }
                while (true)
                {
                    SkipWs();
                    string key = ParseString();
                    SkipWs();
                    _i++; // ':'
                    d[key] = ParseValue();
                    SkipWs();
                    char c = _s[_i++];
                    if (c == '}') break;   // else ',' — continue
                }
                return d;
            }

            private List<object> ParseArray()
            {
                List<object> l = new List<object>();
                _i++; // '['
                SkipWs();
                if (_s[_i] == ']') { _i++; return l; }
                while (true)
                {
                    l.Add(ParseValue());
                    SkipWs();
                    char c = _s[_i++];
                    if (c == ']') break;   // else ',' — continue
                }
                return l;
            }

            private string ParseString()
            {
                StringBuilder sb = new StringBuilder();
                _i++; // opening '"'
                while (true)
                {
                    char c = _s[_i++];
                    if (c == '"') break;
                    if (c != '\\') { sb.Append(c); continue; }
                    char e = _s[_i++];
                    switch (e)
                    {
                        case '"': sb.Append('"'); break;
                        case '\\': sb.Append('\\'); break;
                        case '/': sb.Append('/'); break;
                        case 'b': sb.Append('\b'); break;
                        case 'f': sb.Append('\f'); break;
                        case 'n': sb.Append('\n'); break;
                        case 'r': sb.Append('\r'); break;
                        case 't': sb.Append('\t'); break;
                        case 'u':
                            int code = int.Parse(_s.Substring(_i, 4), NumberStyles.HexNumber, CultureInfo.InvariantCulture);
                            sb.Append((char)code);
                            _i += 4;
                            break;
                    }
                }
                return sb.ToString();
            }

            // Returns long for integers, double for anything with a fraction/exponent (mirrors Go's number wire form).
            private object ParseNumber()
            {
                int start = _i;
                bool isFloat = false;
                while (_i < _s.Length)
                {
                    char c = _s[_i];
                    if (c == '-' || c == '+' || (c >= '0' && c <= '9')) { _i++; }
                    else if (c == '.' || c == 'e' || c == 'E') { isFloat = true; _i++; }
                    else break;
                }
                string num = _s.Substring(start, _i - start);
                if (!isFloat)
                {
                    long l;
                    if (long.TryParse(num, NumberStyles.Integer, CultureInfo.InvariantCulture, out l)) return l;
                }
                return double.Parse(num, CultureInfo.InvariantCulture);
            }

            private void SkipWs()
            {
                while (_i < _s.Length)
                {
                    char c = _s[_i];
                    if (c == ' ' || c == '\t' || c == '\n' || c == '\r') _i++;
                    else break;
                }
            }
        }
    }
}
