package iormesh.workload.scenarios;

import java.io.ByteArrayInputStream;
import java.math.BigInteger;
import java.security.GeneralSecurityException;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.KeyStore;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.cert.Certificate;
import java.security.cert.CertificateFactory;
import java.security.spec.ECGenParameterSpec;
import java.time.Duration;
import java.time.Instant;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;

/**
 * http-tls's server identity, and each TLS service's in the microservices
 * scenario (orders, billing): a fresh EC P-256 key pair and a self-signed
 * certificate for 127.0.0.1, made in memory at run time. The private key
 * exists only inside the server's JVM — never in the repository, never in
 * a file — and is new on every run; only the public certificate is handed
 * to the client, which trusts exactly that one certificate (pinned, PLAN.md
 * M0 "HTTP scenarios"). The subject alternative name carries the IP
 * address, so HttpClient's endpoint identification passes for
 * https://127.0.0.1:port without disabling any check.
 */
final class SelfSignedCert {
    private static final String SUBJECT = "iormesh-workload";
    private static final String ECDSA_SHA256 = "1.2.840.10045.4.3.2";
    private static final String COMMON_NAME = "2.5.4.3";
    private static final String SUBJECT_ALT_NAME = "2.5.29.17";
    private static final byte[] LOOPBACK_IP = {127, 0, 0, 1};
    private static final Duration VALIDITY = Duration.ofDays(1);

    private final KeyPair keys;
    private final byte[] der;

    private SelfSignedCert(KeyPair keys, byte[] der) {
        this.keys = keys;
        this.der = der;
    }

    static SelfSignedCert generate() throws GeneralSecurityException {
        KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
        gen.initialize(new ECGenParameterSpec("secp256r1"));
        KeyPair keys = gen.generateKeyPair();
        // Bit 62 set: RFC 5280 wants a positive serial, and 63 random bits
        // alone could be 0.
        BigInteger serial = new BigInteger(63, new SecureRandom()).setBit(62);
        byte[] tbs = tbs(keys, serial, Instant.now());
        Signature signer = Signature.getInstance("SHA256withECDSA");
        signer.initSign(keys.getPrivate());
        signer.update(tbs);
        byte[] cert = Der.seq(tbs, algorithm(), Der.bitString(signer.sign()));
        return new SelfSignedCert(keys, cert);
    }

    /** The certificate, DER-encoded: what the client gets to pin. */
    byte[] der() {
        return der.clone();
    }

    /** The server's TLS context: this certificate and its private key. */
    SSLContext serverContext() throws GeneralSecurityException {
        KeyStore ks = emptyStore();
        // The store lives only in memory, so its password protects nothing;
        // a fresh random one keeps any constant out of the source.
        char[] pass = Long.toHexString(new SecureRandom().nextLong()).toCharArray();
        ks.setKeyEntry(SUBJECT, keys.getPrivate(), pass, new Certificate[] {parse(der)});
        KeyManagerFactory kmf = KeyManagerFactory.getInstance(
                KeyManagerFactory.getDefaultAlgorithm());
        kmf.init(ks, pass);
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(kmf.getKeyManagers(), null, null);
        return ctx;
    }

    /** A client TLS context that trusts certDer and nothing else. */
    static SSLContext clientContext(byte[] certDer) throws GeneralSecurityException {
        KeyStore ks = emptyStore();
        ks.setCertificateEntry(SUBJECT, parse(certDer));
        TrustManagerFactory tmf = TrustManagerFactory.getInstance(
                TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ks);
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(null, tmf.getTrustManagers(), null);
        return ctx;
    }

    /** TBSCertificate (RFC 5280 4.1): v3, issuer = subject, one SAN extension. */
    private static byte[] tbs(KeyPair keys, BigInteger serial, Instant now) {
        byte[] name = name();
        return Der.seq(
                Der.explicit(0, Der.integer(BigInteger.TWO)),
                Der.integer(serial),
                algorithm(),
                name,
                Der.seq(Der.utcTime(now.minus(Duration.ofHours(1))),
                        Der.utcTime(now.plus(VALIDITY))),
                name,
                keys.getPublic().getEncoded(),
                Der.explicit(3, Der.seq(subjectAltName())));
    }

    private static byte[] algorithm() {
        return Der.seq(Der.oid(ECDSA_SHA256));
    }

    private static byte[] name() {
        return Der.seq(Der.tlv(Der.SET, Der.seq(Der.oid(COMMON_NAME), Der.utf8(SUBJECT))));
    }

    /** SAN: iPAddress [7] 127.0.0.1 — the address the client dials. */
    private static byte[] subjectAltName() {
        byte[] names = Der.seq(Der.tlv(0x87, LOOPBACK_IP));
        return Der.seq(Der.oid(SUBJECT_ALT_NAME), Der.octets(names));
    }

    private static Certificate parse(byte[] certDer) throws GeneralSecurityException {
        return CertificateFactory.getInstance("X.509")
                .generateCertificate(new ByteArrayInputStream(certDer));
    }

    private static KeyStore emptyStore() throws GeneralSecurityException {
        KeyStore ks = KeyStore.getInstance("PKCS12");
        try {
            ks.load(null, null);
        } catch (java.io.IOException e) {
            // load(null, …) only initializes an empty store; no I/O happens.
            throw new GeneralSecurityException(e);
        }
        return ks;
    }
}
