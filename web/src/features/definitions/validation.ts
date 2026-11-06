export const namePattern = /^[a-z][a-z0-9-]{1,39}$/;
export const imagePattern = /^\S+@sha256:[0-9a-fA-F]{64}$/;

export const isValidName = (name: string) => namePattern.test(name);
export const isValidImage = (image: string) => imagePattern.test(image.trim());
export const isValidPort = (port: string) => {
  const value = Number(port);
  return port.trim() !== "" && Number.isInteger(value) && value >= 1 && value <= 65535;
};

/** Same rules as the server: a DNS label of 2 to 40 characters that does not end in a hyphen. */
export const domainPattern = /^[a-z](?:[a-z0-9-]*[a-z0-9])$/;
export const isValidDomain = (domain: string) => domain.length >= 2 && domain.length <= 40 && domainPattern.test(domain);
