export const namePattern = /^[a-z][a-z0-9-]{1,39}$/;
export const imagePattern = /^\S+@sha256:[0-9a-fA-F]{64}$/;

export const isValidName = (name: string) => namePattern.test(name);
export const isValidImage = (image: string) => imagePattern.test(image.trim());
export const isValidPort = (port: string) => {
  const value = Number(port);
  return port.trim() !== "" && Number.isInteger(value) && value >= 1 && value <= 65535;
};
