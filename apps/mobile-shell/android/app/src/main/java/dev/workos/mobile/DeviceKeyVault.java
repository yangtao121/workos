package dev.workos.mobile;

import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyStore;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/** Device identity ciphertext only; never falls back when AndroidKeyStore fails. */
final class DeviceKeyVault {
    private static final Object KEY_LOCK = new Object();
    static final String PREFERENCES = "workos_device_vault_v1";
    private final SharedPreferences preferences;
    private final String alias;

    DeviceKeyVault(Context context) {
        this(context, PREFERENCES, context.getPackageName() + ".workos.device.v1");
    }

    DeviceKeyVault(Context context, String preferencesName, String alias) {
        this.preferences = context.getSharedPreferences(preferencesName, Context.MODE_PRIVATE);
        this.alias = alias;
    }

    synchronized void checkAvailable() throws GeneralSecurityException, IOException {
        key();
    }

    private SecretKey key() throws GeneralSecurityException, IOException {
        synchronized (KEY_LOCK) {
            KeyStore store = KeyStore.getInstance("AndroidKeyStore");
            store.load(null);
            if (store.containsAlias(alias)) {
                java.security.Key stored = store.getKey(alias, null);
                if (!(stored instanceof SecretKey) || !"AES".equals(stored.getAlgorithm())) {
                    throw new GeneralSecurityException("device vault key unavailable");
                }
                return (SecretKey) stored;
            }
            // A lost key must not silently reset identities that still have ciphertext.
            if (!preferences.getAll().isEmpty()) {
                throw new GeneralSecurityException("device vault key unavailable");
            }
            KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setKeySize(256)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build());
            return generator.generateKey();
        }
    }

    private void validateSlot(String slot) {
        if (slot == null || slot.isEmpty() || slot.length() > 1024) {
            throw new IllegalArgumentException("invalid device vault slot");
        }
    }

    synchronized void set(String slot, String value) throws GeneralSecurityException, IOException {
        validateSlot(slot);
        if (value == null || value.getBytes(StandardCharsets.UTF_8).length > 16384) {
            throw new IllegalArgumentException("invalid device vault value");
        }
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.ENCRYPT_MODE, key());
        cipher.updateAAD(slot.getBytes(StandardCharsets.UTF_8));
        byte[] encrypted = cipher.doFinal(value.getBytes(StandardCharsets.UTF_8));
        String record = "v1." + Base64.encodeToString(cipher.getIV(), Base64.NO_WRAP)
            + "." + Base64.encodeToString(encrypted, Base64.NO_WRAP);
        if (!preferences.edit().putString(slot, record).commit()) {
            throw new IOException("device vault write failed");
        }
    }

    synchronized String get(String slot) throws GeneralSecurityException, IOException {
        validateSlot(slot);
        String record = preferences.getString(slot, null);
        if (record == null) return null;
        if (record.length() > 24000) throw new GeneralSecurityException("invalid device vault record");
        try {
            String[] parts = record.split("\\.", -1);
            if (parts.length != 3 || !parts[0].equals("v1")) {
                throw new GeneralSecurityException("invalid device vault record");
            }
            byte[] iv = Base64.decode(parts[1], Base64.NO_WRAP);
            byte[] ciphertext = Base64.decode(parts[2], Base64.NO_WRAP);
            if (iv.length != 12 || ciphertext.length < 16 || ciphertext.length > 16400) {
                throw new GeneralSecurityException("invalid device vault record");
            }
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, iv));
            // Binding AAD to the origin-scoped slot prevents ciphertext swapping.
            cipher.updateAAD(slot.getBytes(StandardCharsets.UTF_8));
            return new String(cipher.doFinal(ciphertext), StandardCharsets.UTF_8);
        } catch (IllegalArgumentException error) {
            throw new GeneralSecurityException("invalid device vault record");
        }
    }

    synchronized void remove(String slot) throws IOException {
        validateSlot(slot);
        if (!preferences.edit().remove(slot).commit()) {
            throw new IOException("device vault removal failed");
        }
    }
}
