package iormesh.workload.scenarios;

import java.io.ByteArrayOutputStream;
import java.math.BigInteger;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;

/**
 * The few DER encodings http-tls needs to build its self-signed X.509
 * certificate ({@link SelfSignedCert}). The JDK can generate a key pair
 * and sign bytes but has no public API that builds a certificate
 * (sun.security.x509 is not exported), and keytool would mean a child
 * process and a key file on disk inside the scenario's bracket — so the
 * workload encodes the handful of ASN.1 types itself. Definite lengths
 * only, as DER requires.
 */
final class Der {
    static final int SEQUENCE = 0x30;
    static final int SET = 0x31;
    private static final int INTEGER = 0x02;
    private static final int BIT_STRING = 0x03;
    private static final int OCTET_STRING = 0x04;
    private static final int OID = 0x06;
    private static final int UTF8_STRING = 0x0c;
    private static final int UTC_TIME = 0x17;
    private static final DateTimeFormatter UTC =
            DateTimeFormatter.ofPattern("yyMMddHHmmss'Z'").withZone(ZoneOffset.UTC);

    private Der() {}

    /** Tag, length, then the concatenated parts. */
    static byte[] tlv(int tag, byte[]... parts) {
        ByteArrayOutputStream body = new ByteArrayOutputStream();
        for (byte[] p : parts) {
            body.writeBytes(p);
        }
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        out.write(tag);
        out.writeBytes(length(body.size()));
        out.writeBytes(body.toByteArray());
        return out.toByteArray();
    }

    static byte[] seq(byte[]... parts) {
        return tlv(SEQUENCE, parts);
    }

    /** INTEGER; BigInteger's two's-complement bytes are already minimal. */
    static byte[] integer(BigInteger v) {
        return tlv(INTEGER, v.toByteArray());
    }

    static byte[] utf8(String s) {
        return tlv(UTF8_STRING, s.getBytes(StandardCharsets.UTF_8));
    }

    /** UTCTime, valid for the years 1950–2049 this certificate uses. */
    static byte[] utcTime(Instant t) {
        return tlv(UTC_TIME, UTC.format(t).getBytes(StandardCharsets.US_ASCII));
    }

    /** BIT STRING with no unused bits (signatures). */
    static byte[] bitString(byte[] bits) {
        return tlv(BIT_STRING, new byte[] {0}, bits);
    }

    static byte[] octets(byte[] b) {
        return tlv(OCTET_STRING, b);
    }

    /** A constructed, explicitly tagged context field: [n] EXPLICIT. */
    static byte[] explicit(int n, byte[] inner) {
        return tlv(0xa0 | n, inner);
    }

    /** OBJECT IDENTIFIER from its dotted form, e.g. "2.5.4.3". */
    static byte[] oid(String dotted) {
        String[] arcs = dotted.split("\\.");
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        // The first two arcs share one subidentifier (X.690 8.19.4).
        base128(out, Long.parseLong(arcs[0]) * 40 + Long.parseLong(arcs[1]));
        for (int i = 2; i < arcs.length; i++) {
            base128(out, Long.parseLong(arcs[i]));
        }
        return tlv(OID, out.toByteArray());
    }

    /** One subidentifier, big-endian base 128, high bit on all but the last. */
    private static void base128(ByteArrayOutputStream out, long v) {
        int shift = 63 - Long.numberOfLeadingZeros(v | 1);
        for (int s = shift / 7 * 7; s > 0; s -= 7) {
            out.write((int) ((v >>> s) & 0x7f) | 0x80);
        }
        out.write((int) (v & 0x7f));
    }

    /** Short form below 128, else the long form with minimal octets. */
    private static byte[] length(int n) {
        if (n < 0x80) {
            return new byte[] {(byte) n};
        }
        byte[] digits = BigInteger.valueOf(n).toByteArray();
        int skip = digits[0] == 0 ? 1 : 0;
        byte[] out = new byte[digits.length - skip + 1];
        out[0] = (byte) (0x80 | (digits.length - skip));
        System.arraycopy(digits, skip, out, 1, digits.length - skip);
        return out;
    }
}
